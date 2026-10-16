// Command admin manages back-office accounts from the server (DATABASE_URL):
//
//	admin create -username nc -email nc@example.com [-name 倪] [-roles super_admin] [-password-stdin]
//	admin list
//	admin unlock -username nc
//
// create bootstraps an active admin: the password is read from the terminal
// without echo (twice), or from one line of stdin with -password-stdin; it
// is never printed or logged. It prints the TOTP enrollment URI (scan it
// into an authenticator app; the first sign-in with a code completes
// enrollment) and ten one-time recovery codes. Everyone after the first
// admin is invited from the back office (/admin/users) instead.
//
// Set ADMIN_TOTP_KEY to the same value as the API so the TOTP secret is
// sealed with it.
package main

import (
	"avxxx/internal/adminauth"
	"avxxx/internal/store"
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "admin:", err)
		os.Exit(1)
	}
}

const usage = `usage:
  admin create -username NAME -email EMAIL [-name DISPLAY] [-roles super_admin,...] [-password-stdin]
  admin list
  admin unlock -username NAME`

func run(args []string, stdin *os.File, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	ctx := context.Background()
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		username := fs.String("username", "", "login name and review_events actor (a-z 0-9 . _ -)")
		email := fs.String("email", "", "email (also accepted as login)")
		name := fs.String("name", "", "display name")
		roles := fs.String("roles", string(adminauth.RoleSuperAdmin), "comma-separated roles: "+roleList())
		fromStdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin instead of the terminal")
		fs.SetOutput(stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *username == "" || *email == "" {
			return errors.New(usage)
		}
		in := store.AdminUserInput{Username: *username, Email: *email, DisplayName: *name, Roles: strings.Split(*roles, ",")}
		if _, ok := adminauth.NormalizeRoles(in.Roles); !ok {
			return fmt.Errorf("unknown role in %q; roles: %s", *roles, roleList())
		}
		password, err := readPassword(stdin, stderr, *fromStdin)
		if err != nil {
			return err
		}
		if os.Getenv(adminauth.KeyEnv) == "" {
			fmt.Fprintf(stderr, "warning: %s is not set; the TOTP secret is stored unencrypted until it is\n", adminauth.KeyEnv)
		}
		s, err := store.Open(ctx)
		if err != nil {
			return err
		}
		defer s.Close()
		if n, err := s.CountAdmins(ctx); err == nil && n == 0 && !adminauth.IsSuper(in.Roles) {
			fmt.Fprintln(stderr, "warning: the first admin has no super_admin role; nobody will be able to manage admin accounts")
		}
		u, secret, codes, err := s.BootstrapAdmin(ctx, in, password)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Created admin %q (id %d, roles %s).\n\n", u.Username, u.ID, strings.Join(u.Roles, ","))
		fmt.Fprintln(stdout, "Two-step verification: add this to an authenticator app (scan it as a QR code or paste it):")
		fmt.Fprintf(stdout, "  %s\n", adminauth.URI(u.Email, secret))
		fmt.Fprintf(stdout, "  secret for manual entry: %s\n\n", secret)
		fmt.Fprintln(stdout, "Recovery codes (each works once; store them offline, they are not shown again):")
		for _, c := range codes {
			fmt.Fprintf(stdout, "  %s\n", c)
		}
		fmt.Fprintln(stdout, "\nSign in at /admin/login with the username or email, the password and a 6-digit code.")
		return nil
	case "list":
		s, err := store.Open(ctx)
		if err != nil {
			return err
		}
		defer s.Close()
		list, err := s.AdminUsers(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tUSERNAME\tEMAIL\tSTATUS\tROLES\t2FA\tLOCKED UNTIL")
		for _, u := range list {
			locked := ""
			if u.LockedUntil != nil {
				locked = u.LockedUntil.Format("2006-01-02 15:04:05Z07:00")
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%v\t%s\n", u.ID, u.Username, u.Email, u.Status, strings.Join(u.Roles, ","), u.TOTPEnrolled, locked)
		}
		return tw.Flush()
	case "unlock":
		fs := flag.NewFlagSet("unlock", flag.ContinueOnError)
		username := fs.String("username", "", "admin to unlock")
		fs.SetOutput(stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *username == "" {
			return errors.New(usage)
		}
		s, err := store.Open(ctx)
		if err != nil {
			return err
		}
		defer s.Close()
		if err := s.UnlockAdmin(ctx, *username); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Unlocked %q.\n", *username)
		return nil
	}
	return errors.New(usage)
}

func roleList() string {
	out := make([]string, len(adminauth.Roles))
	for i, r := range adminauth.Roles {
		out[i] = string(r)
	}
	return strings.Join(out, ",")
}

// readPassword reads the password without echo from the terminal (twice),
// or one line from stdin with -password-stdin.
func readPassword(stdin *os.File, stderr io.Writer, fromStdin bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fd := int(stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("stdin is not a terminal: run with -it, or pass -password-stdin")
	}
	fmt.Fprintf(stderr, "Password (at least %d characters): ", store.MinAdminPasswordLen)
	a, err := term.ReadPassword(fd)
	fmt.Fprintln(stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(stderr, "Repeat password: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(stderr)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", errors.New("passwords do not match")
	}
	return string(a), nil
}
