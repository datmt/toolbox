package containers

import (
	"context"
	"strings"
	"testing"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/runner"
)

const dfJSON = `{"Images":[
 {"ID":"sha256:aaa111aaa111aaa","Repository":"redis","Tag":"latest","Size":"140MB","Containers":"0","CreatedSince":"2 weeks ago"},
 {"ID":"sha256:bbb","Repository":"postgres","Tag":"16","Size":"276MB","Containers":"2","CreatedSince":"1 week ago"},
 {"ID":"sha256:ccc222ccc222ccc","Repository":"<none>","Tag":"<none>","Size":"1.2GB","Containers":"0","CreatedSince":"1 month ago"}],
"Containers":[
 {"ID":"c1","Names":"web","Image":"nginx","State":"running","Status":"Up 1h","Size":"2B","CreatedAt":"2026-10-11 07:47:25 +0700"},
 {"ID":"c2","Names":"old","Image":"redis","State":"exited","Status":"Exited (0) 3 days ago","Size":"10kB","CreatedAt":"2026-10-01 01:00:00 +0700"}],
"Volumes":[
 {"Name":"used","Size":"1MB","Links":"1","Labels":""},
 {"Name":"proj_data","Size":"3.721kB","Links":"0","Labels":"a=b,com.docker.compose.project=proj"}]}`

func fake() (*Docker, *runner.Fake) {
	f := &runner.Fake{Out: map[string]string{"docker system df -v --format {{json .}}": dfJSON}}
	return NewDocker(f), f
}

func scan(t *testing.T, c core.Category) []core.Item {
	t.Helper()
	ch := make(chan core.Item, 4096)
	if err := c.Scanner().Scan(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	var out []core.Item
	for it := range ch {
		out = append(out, it)
	}
	return out
}

func TestCategories(t *testing.T) {
	d, f := fake()
	cats := d.Categories()

	cs := scan(t, cats[0])
	if len(cs) != 1 || cs[0].ID != "c2" || cs[0].Size != 10000 {
		t.Errorf("containers = %+v", cs)
	}
	im := scan(t, cats[1])
	if len(im) != 2 || im[0].ID != "redis:latest" || im[0].Size != 140_000_000 || im[1].ID != "ccc222ccc222ccc" {
		t.Errorf("images = %+v", im)
	}
	vs := scan(t, cats[2])
	if len(vs) != 1 || vs[0].ID != "proj_data" || vs[0].Risk != core.Danger || !strings.Contains(vs[0].Desc, "proj") {
		t.Errorf("volumes = %+v", vs)
	}
	if len(f.Calls) != 1 {
		t.Errorf("df called %d times, want 1 (snapshot shared)", len(f.Calls))
	}
}

func TestRemoveNeverForces(t *testing.T) {
	d, f := fake()
	cats := d.Categories()
	im := scan(t, cats[1])
	f.Calls = nil
	res := cats[1].Remove(im).Do(context.Background())
	if len(res.Done) != 2 {
		t.Fatalf("%+v", res)
	}
	for _, c := range f.Calls {
		for _, a := range c {
			if a == "-f" || a == "--force" {
				t.Errorf("forced: %v", c)
			}
		}
	}
	if got := strings.Join(f.Calls[0], " "); got != "docker rmi redis:latest" {
		t.Errorf("argv = %s", got)
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		s    string
		dec  bool
		want int64
	}{{"788MB", true, 788_000_000}, {"3.721kB", true, 3721}, {"2B", true, 2}, {"1.5G", false, 1610612736}, {"512M", false, 512 << 20}, {"7", false, 7}, {"junk", true, 0}}
	for _, c := range cases {
		if got := core.ParseSize(c.s, c.dec); got != c.want {
			t.Errorf("%q = %d, want %d", c.s, got, c.want)
		}
	}
}
