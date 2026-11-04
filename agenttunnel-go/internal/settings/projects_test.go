package settings

import (
	"path/filepath"
	"testing"
)

func TestProjectsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := Open(path, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	s.AddProject(Project{ID: "p1", Name: "Alpha", Path: "/tmp/alpha"})
	s.AddProject(Project{ID: "p2", Name: "Beta", Path: "/tmp/beta"})

	again, err := Open(path, "")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := again.Projects(); len(got) != 2 {
		t.Fatalf("projects = %+v", got)
	}
	if p, ok := again.Project("p1"); !ok || p.Path != "/tmp/alpha" || p.Name != "Alpha" {
		t.Fatalf("p1 = %+v ok=%v", p, ok)
	}

	again.RemoveProject("p1")
	third, err := Open(path, "")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := third.Projects(); len(got) != 1 || got[0].ID != "p2" {
		t.Fatalf("after remove = %+v", got)
	}
}

func TestAddProjectReplacesSameID(t *testing.T) {
	s := New("")
	s.AddProject(Project{ID: "p1", Name: "Alpha", Path: "/tmp/a"})
	s.AddProject(Project{ID: "p1", Name: "Renamed", Path: "/tmp/b"})

	got := s.Projects()
	if len(got) != 1 || got[0].Name != "Renamed" || got[0].Path != "/tmp/b" {
		t.Fatalf("projects = %+v", got)
	}
}

func TestAddProjectRequiresIDAndName(t *testing.T) {
	s := New("")
	s.AddProject(Project{ID: "", Name: "Alpha"})
	s.AddProject(Project{ID: "p1", Name: ""})
	if got := s.Projects(); len(got) != 0 {
		t.Fatalf("invalid projects were accepted: %+v", got)
	}
}
