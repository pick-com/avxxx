INSERT INTO sources(name,url,enabled,adapter,source_kind,license_status)
VALUES ('Demo Catalog','https://example.invalid/demo',true,'rss','fixture','internal')
ON CONFLICT(url) DO UPDATE SET enabled=true;
INSERT INTO performers(slug,canonical_name,canonical_name_ja,status)
VALUES ('demo-actress','Demo Actress','デモ女優','active')
ON CONFLICT(slug) DO UPDATE SET canonical_name=EXCLUDED.canonical_name;
INSERT INTO releases(slug,canonical_title,title_ja,maker_name,status)
VALUES ('demo-release','Demo Release','デモ作品','Demo Maker','published')
ON CONFLICT(slug) DO UPDATE SET canonical_title=EXCLUDED.canonical_title;
INSERT INTO release_performers(release_id,performer_id,billing_order,is_primary)
SELECT r.id,p.id,0,true FROM releases r,performers p WHERE r.slug='demo-release' AND p.slug='demo-actress'
ON CONFLICT(release_id,performer_id) DO NOTHING;
INSERT INTO release_tags(release_id,tag,normalized_tag)
SELECT id,'Demo','demo' FROM releases WHERE slug='demo-release'
ON CONFLICT(release_id,normalized_tag) DO NOTHING;
