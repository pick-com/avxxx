# JAVLibrary fixtures (SYNTHETIC)

**Every file in this directory is synthetic.** No page of www.javlibrary.com
was saved to write them: on 2026-10-16 every request from our egress,
`robots.txt` included, got a Cloudflare managed challenge (HTTP 403,
`cf-mitigated: challenge`). The markup follows the site's long-standing
layout (`/en/?v=<id>` release pages with `#video_id`, `#video_date`,
`#video_length`, `#video_director`, `#video_maker`, `#video_label`,
`#video_review .score`, `#video_genres`, `#video_cast`; listings with
`.videos .video a` and `.page_selector a.page.next`). Titles, codes (`ZZZ-*`)
and names are fictional; every manifest row has `"origin": "synthetic"`.

Release pages map to no `performers` (the cast is evidence only), so the
report shows 4 of 5 required release fields by design.

| file | entity | what it covers |
| --- | --- | --- |
| `discovery/newrelease_page1.html` | discovery | two releases, a duplicate link, a star link and a menu link (ignored), next page |
| `discovery/newrelease_page2.html` | discovery | last page (only a prev link) |
| `releases/javlizzzz1.html` | release | every field, rating `(8.30)`, two cast members |
| `releases/javlizzzz2.html` | release | no label / director / rating |
| `releases/no_title.html` | release (expect skip) | no title block |
