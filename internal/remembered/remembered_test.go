package remembered

import "testing"

func TestFill(t *testing.T) {
	mem := Target{Host: "testhost", Database: "testdb"}
	cases := []struct {
		name           string
		ok             bool
		host, database string
		wantH, wantD   string
		wantUsed       bool
	}{
		{"nothing given", true, "", "", "testhost", "testdb", true},
		{"the remembered host given", true, "testhost", "", "testhost", "testdb", true},
		{"another host given", true, "other", "", "other", "", false},
		{"only a database given", true, "", "x", "testhost", "x", true},
		{"both given", true, "other", "x", "other", "x", false},
		{"nothing remembered", false, "", "", "", "", false},
	}
	for _, c := range cases {
		h, d, used := Fill(mem, c.ok, c.host, c.database)
		if h != c.wantH || d != c.wantD || used != c.wantUsed {
			t.Errorf("%s: Fill = (%q, %q, %v), want (%q, %q, %v)", c.name, h, d, used, c.wantH, c.wantD, c.wantUsed)
		}
	}
}

func TestNext(t *testing.T) {
	prev := Target{Host: "testhost", Database: "testdb"}
	cases := []struct {
		name      string
		ok        bool
		g         Given
		want      Target
		wantWrite bool
	}{
		{"the same host flagged", true, Given{Host: "testhost", HostFlag: true}, Target{}, false},
		{"another host flagged", true, Given{Host: "other", HostFlag: true}, Target{Host: "other"}, true},
		{"another host and a database flagged", true, Given{Host: "other", HostFlag: true, Database: "x", DatabaseFlag: true}, Target{Host: "other", Database: "x"}, true},
		{"a database flagged on the remembered host", true, Given{Database: "y", DatabaseFlag: true}, Target{Host: "testhost", Database: "y"}, true},
		{"nothing flagged", true, Given{}, Target{}, false},
		{"host from the environment", true, Given{Host: "other"}, Target{}, false},
		{"host from the environment, database flagged", true, Given{Host: "other", Database: "y", DatabaseFlag: true}, Target{}, false},
		{"nothing remembered, host flagged", false, Given{Host: "a", HostFlag: true}, Target{Host: "a"}, true},
		{"nothing remembered, only a database flagged", false, Given{Database: "y", DatabaseFlag: true}, Target{}, false},
	}
	for _, c := range cases {
		got, write := Next(prev, c.ok, c.g)
		if write != c.wantWrite {
			t.Errorf("%s: write = %v, want %v", c.name, write, c.wantWrite)
			continue
		}
		if write && got != c.want {
			t.Errorf("%s: Next = %+v, want %+v", c.name, got, c.want)
		}
	}
}
