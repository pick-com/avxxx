// Package images stores catalog images (release covers, performer
// portraits) from image-permitted sources in object storage and removes
// them again on takedown (docs/DATA_RETENTION_AND_IMAGES.md):
//
//   - policy.go: which source and which asset type may be stored at all;
//   - process.go: validation, size cap and derived sizes (pure Go);
//   - fetch.go: the media_fetch job (cmd/audit worker, cmd/scheduler);
//   - purge.go: the media_purge job;
//   - urls.go: public image URLs (API proxy, CDN base or presigned).
package images

import (
	"fmt"
	"time"

	"avxxx/internal/catalog"
)

// Job kinds (sourceless jobs; cmd/scheduler enqueues, cmd/audit runs).
const (
	JobKindFetch = "media_fetch"
	JobKindPurge = "media_purge"
)

// Entity and asset types (media_assets CHECK constraints).
const (
	EntityRelease   = "release"
	EntityPerformer = "performer"

	AssetCover  = "cover"
	AssetAvatar = "avatar"
)

// Variants of one stored image.
const (
	VariantOriginal = "original"
	VariantW320     = "w320"
	VariantW720     = "w720"
)

// ImageLicenses are the license statuses whose terms can cover storing and
// re-serving images. terms_confirmed only covers metadata crawling.
var ImageLicenses = map[string]bool{catalog.LicenseOfficialAPI: true, catalog.LicenseLicensed: true, catalog.LicenseInternal: true}

// StoredAssetTypes is the asset type the fetch job stores per entity type.
// Sample images, galleries and preview video are never stored.
var StoredAssetTypes = map[string]string{EntityRelease: AssetCover, EntityPerformer: AssetAvatar}

// SourcePolicy is the subset of a sources row the image policy reads.
type SourcePolicy struct {
	ID               int64      `json:"id"`
	Name             string     `json:"name,omitempty"`
	Adapter          string     `json:"adapter"`
	LicenseStatus    string     `json:"license_status"`
	TermsReviewedAt  *time.Time `json:"terms_reviewed_at,omitempty"`
	ImagesAllowed    bool       `json:"images_allowed"`
	ImagesReviewedAt *time.Time `json:"images_reviewed_at,omitempty"`
	ImagesReviewedBy string     `json:"images_reviewed_by,omitempty"`
	ImagesNote       string     `json:"images_note,omitempty"`
}

// Decision is the outcome of SourceAllowed.
type Decision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// LicensePermitsImages reports whether a license status can be marked
// images_allowed at all (the admin endpoint refuses the others).
func LicensePermitsImages(status string) bool { return ImageLicenses[status] }

// SourceAllowed decides whether images from src may be downloaded and
// stored: an image-capable license, a recorded terms review, and an
// explicit, reviewed images_allowed flag. Feeds never qualify. It is
// evaluated on every run, so a later license downgrade stops downloads even
// while images_allowed is still set.
func SourceAllowed(src SourcePolicy) Decision {
	switch {
	case src.Adapter == "" || src.Adapter == "rss":
		return Decision{Reason: "feed sources never provide stored images"}
	case !ImageLicenses[src.LicenseStatus]:
		return Decision{Reason: fmt.Sprintf("license_status %q does not cover images (need official_api, licensed or internal)", src.LicenseStatus)}
	case src.TermsReviewedAt == nil:
		return Decision{Reason: "terms have not been reviewed"}
	case !src.ImagesAllowed:
		return Decision{Reason: "images_allowed is off for this source"}
	case src.ImagesReviewedAt == nil:
		return Decision{Reason: "images_allowed has no recorded review"}
	}
	return Decision{Allowed: true}
}

// AssetAllowed reports whether assetType is the stored asset of entityType.
func AssetAllowed(entityType, assetType string) bool {
	return assetType != "" && StoredAssetTypes[entityType] == assetType
}
