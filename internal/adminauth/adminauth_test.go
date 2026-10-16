package adminauth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B: 8-digit codes for the three reference seeds.
func TestTOTPRFC6238Vectors(t *testing.T) {
	seeds := map[string][]byte{
		"SHA1":   []byte("12345678901234567890"),
		"SHA256": []byte("12345678901234567890123456789012"),
		"SHA512": []byte("1234567890123456789012345678901234567890123456789012345678901234"),
	}
	vectors := []struct {
		unix int64
		alg  string
		want string
	}{
		{59, "SHA1", "94287082"}, {59, "SHA256", "46119246"}, {59, "SHA512", "90693936"},
		{1111111109, "SHA1", "07081804"}, {1111111109, "SHA256", "68084774"}, {1111111109, "SHA512", "25091201"},
		{1111111111, "SHA1", "14050471"}, {1111111111, "SHA256", "67062674"}, {1111111111, "SHA512", "99943326"},
		{1234567890, "SHA1", "89005924"}, {1234567890, "SHA256", "91819424"}, {1234567890, "SHA512", "93441116"},
		{2000000000, "SHA1", "69279037"}, {2000000000, "SHA256", "90698825"}, {2000000000, "SHA512", "38618901"},
		{20000000000, "SHA1", "65353130"}, {20000000000, "SHA256", "77737706"}, {20000000000, "SHA512", "47863826"},
	}
	for _, v := range vectors {
		got := TOTP(seeds[v.alg], time.Unix(v.unix, 0).UTC(), 8, v.alg)
		if got != v.want {
			t.Errorf("T=%d %s: got %s want %s", v.unix, v.alg, got, v.want)
		}
	}
}

// RFC 4226 Appendix D: HOTP values for counters 0..9 (6 digits, SHA-1).
func TestHOTPRFC4226Vectors(t *testing.T) {
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for i, w := range want {
		if got := HOTP([]byte("12345678901234567890"), uint64(i), 6, Hash("SHA1")); got != w {
			t.Errorf("counter %d: %s want %s", i, got, w)
		}
	}
}

func TestVerifyWindowAndFormat(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	now := time.Unix(1111111111, 0)
	cur, _ := Code(secret, now)
	prev, _ := Code(secret, now.Add(-30*time.Second))
	next, _ := Code(secret, now.Add(30*time.Second))
	old, _ := Code(secret, now.Add(-90*time.Second))
	if step, ok := Verify(secret, cur, now); !ok || step != Step(now) {
		t.Errorf("current code rejected (%d %v)", step, ok)
	}
	if step, ok := Verify(secret, prev, now); !ok || step != Step(now)-1 {
		t.Error("previous step should be accepted")
	}
	if _, ok := Verify(secret, next, now); !ok {
		t.Error("next step should be accepted")
	}
	if _, ok := Verify(secret, old, now); ok && old != cur && old != prev && old != next {
		t.Error("code three steps old accepted")
	}
	if _, ok := Verify(secret, " "+cur[:3]+" "+cur[3:], now); !ok {
		t.Error("spaces should be ignored")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := Verify(secret, bad, now); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, ok := Verify("!!!", cur, now); ok {
		t.Error("invalid secret accepted")
	}
}

func TestNewSecretAndURI(t *testing.T) {
	s, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	k, err := DecodeSecret(strings.ToLower(s))
	if err != nil || len(k) != SecretBytes {
		t.Fatalf("decode %q: %v (%d bytes)", s, err, len(k))
	}
	u := URI("ana@example.com", s)
	for _, want := range []string{"otpauth://totp/REEL%20Admin:ana@example.com?", "secret=" + s, "issuer=REEL+Admin", "digits=6", "period=30", "algorithm=SHA1"} {
		if !strings.Contains(u, want) {
			t.Errorf("URI %q lacks %q", u, want)
		}
	}
}

func TestSealer(t *testing.T) {
	plain := NewSealer("")
	st, _ := plain.Seal("ABC")
	if st != "plain:ABC" {
		t.Fatalf("plain seal %q", st)
	}
	keyed := NewSealer("k1")
	got, isPlain, err := keyed.Open(st)
	if err != nil || got != "ABC" || !isPlain {
		t.Fatalf("open plain with key: %q %v %v", got, isPlain, err)
	}
	sealed, _ := keyed.Seal("SECRET")
	if strings.Contains(sealed, "SECRET") || !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("sealed %q", sealed)
	}
	if got, isPlain, err := keyed.Open(sealed); err != nil || got != "SECRET" || isPlain {
		t.Fatalf("round trip: %q %v %v", got, isPlain, err)
	}
	if _, _, err := plain.Open(sealed); err != ErrNoKey {
		t.Errorf("open without key: %v", err)
	}
	if _, _, err := NewSealer("k2").Open(sealed); err == nil {
		t.Error("wrong key opened the secret")
	}
	a, _ := keyed.Seal("SECRET")
	if a == sealed {
		t.Error("nonce reused")
	}
}

func TestLockoutSchedule(t *testing.T) {
	cases := map[int]time.Duration{0: 0, 4: 0, 5: time.Minute, 6: 2 * time.Minute, 7: 4 * time.Minute, 11: LockoutMax, 50: LockoutMax}
	for n, want := range cases {
		if got := LockoutDuration(n); got != want {
			t.Errorf("LockoutDuration(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, err := NewRecoveryCodes(RecoveryCodeCount)
	if err != nil || len(codes) != RecoveryCodeCount {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 11 || c[5] != '-' || seen[c] {
			t.Errorf("bad or duplicate code %q", c)
		}
		seen[c] = true
		if NormalizeRecoveryCode(strings.ToUpper(strings.ReplaceAll(c, "-", " "))) != c {
			t.Errorf("normalize %q", c)
		}
	}
	if tok, _ := NewToken("x_"); !strings.HasPrefix(tok, "x_") || len(tok) < 40 {
		t.Errorf("token %q", tok)
	}
	if HashToken("a") == HashToken("b") || len(HashToken("a")) != 64 {
		t.Error("hash token")
	}
}

func TestRoleMatrix(t *testing.T) {
	if len(Roles) != 7 || len(RoleLabels) != 7 {
		t.Fatalf("want the 7 roles of artboard AdminUsers, have %d", len(Roles))
	}
	for _, g := range Groups {
		if RoleAccess(RoleSuperAdmin, g) != Write {
			t.Errorf("super_admin lacks write on %s", g)
		}
		if GroupLabels[g] == "" {
			t.Errorf("group %s has no label", g)
		}
	}
	cases := []struct {
		role  Role
		group Group
		want  Access
	}{
		{RoleTranslator, GroupI18n, Write},
		{RoleTranslator, GroupCatalog, None},
		{RoleTranslator, GroupDashboard, None},
		{RoleEditor, GroupCatalog, Write},
		{RoleEditor, GroupIngest, Read},
		{RoleEditor, GroupAdminUsers, None},
		{RoleIngestOps, GroupIngest, Write},
		{RoleIngestOps, GroupModeration, None},
		{RoleModerator, GroupModeration, Write},
		{RoleModerator, GroupSiteUsers, Write},
		{RoleModerator, GroupOps, None},
		{RoleModerator, GroupCompliance, Write},
		{RoleModerator, GroupLegal, Read},
		{RoleEditor, GroupLegal, None},
		{RoleOperations, GroupOps, Write},
		{RoleOperations, GroupModeration, Read},
		{RoleAdSales, GroupAds, Write},
		{RoleAdSales, GroupCatalog, None},
		{RoleAdSales, GroupSelf, Write},
	}
	for _, c := range cases {
		if got := RoleAccess(c.role, c.group); got != c.want {
			t.Errorf("%s on %s = %v, want %v", c.role, c.group, got, c.want)
		}
	}
	// Only super_admin reaches admin accounts and the op log.
	for _, r := range Roles[1:] {
		if RoleAccess(r, GroupAdminUsers) != None || RoleAccess(r, GroupAuditLog) != None {
			t.Errorf("%s reaches admin accounts or the op log", r)
		}
	}
	if AccessOf([]string{"translator", "moderator"}, GroupModeration) != Write {
		t.Error("access is the union of roles")
	}
	if AccessOf([]string{"root"}, GroupCatalog) != None {
		t.Error("unknown role granted access")
	}
	if p := Permissions([]string{"translator"}); strings.Join(p, ",") != "i18n:read,i18n:write" {
		t.Errorf("translator permissions %v", p)
	}
	if got, ok := NormalizeRoles([]string{"translator", "super_admin", "translator"}); !ok || strings.Join(got, ",") != "super_admin,translator" {
		t.Errorf("normalize %v %v", got, ok)
	}
	if _, ok := NormalizeRoles([]string{"root"}); ok {
		t.Error("unknown role accepted")
	}
	if m := Matrix(); len(m) != len(Groups) || m[0].Access["super_admin"] != "write" {
		t.Errorf("matrix %+v", m[0])
	}
}
