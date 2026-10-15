package catalog

import (
	"context"
	"testing"
)

func TestJAVDatabaseRelease(t *testing.T) {
	doc := Document{FinalURL: "https://www.javdatabase.com/movies/juq-743/", Body: []byte(`<html><body><h1>JUQ-743 - Example</h1><p>DVD ID: JUQ-743</p><p>Content ID: juq00743</p><p>Release Date: 2024-05-24</p><p>Runtime: 150 min.</p><p>Studio: <a>MADONNA</a></p><p>Director: <a>Director X</a></p><p>Genre(s): <a>Mature</a><a>Creampie</a></p><p>Idol(s)/Actress(es): <a>Yurine Tsukino</a></p></body></html>`)}
	r, e := JAVDatabaseAdapter{}.Parse(context.Background(), doc, Release)
	if e != nil {
		t.Fatal(e)
	}
	if r.Fields["dvd_code"] != "JUQ-743" || r.Fields["content_id"] != "juq00743" {
		t.Fatalf("fields: %#v", r.Fields)
	}
}

func TestJAVDatabasePerformer(t *testing.T) {
	doc := Document{FinalURL: "https://www.javdatabase.com/idols/sena-hashimoto/", Body: []byte(`<html><body><h1>Sena Hashimoto - JAV Profile</h1><p>DOB: 2006-05-10</p><p>Debut: 2026-11-05</p><p>Measurements: 83-55-85</p><p>Cup: D</p><p>Height: 158 cm</p><p>Tags: <a>Twenties</a></p></body></html>`)}
	r, e := JAVDatabaseAdapter{}.Parse(context.Background(), doc, Performer)
	if e != nil {
		t.Fatal(e)
	}
	if r.Fields["name"] != "Sena Hashimoto" || r.Fields["cup_size"] != "D" {
		t.Fatalf("fields: %#v", r.Fields)
	}
}
