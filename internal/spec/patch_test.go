package spec

import "testing"

func TestPatchClears(t *testing.T) {
	cur := &App{Name: "web", Image: "web:1", Port: 80, HostPort: 8090, Memory: "512m"}
	a, err := (&Patch{Name: "web", RemoveHostPort: true, RemoveMemory: true}).Apply(cur)
	if err != nil || a.HostPort != 0 || a.Memory != "" || cur.HostPort != 8090 {
		t.Fatalf("%+v %v", a, err)
	}
}
