//go:build !windows

package content

import (
	"os"
	"reflect"
)

func singleLink(_ *os.File, fi os.FileInfo) error {
	v := reflect.Indirect(reflect.ValueOf(fi.Sys()))
	if v.IsValid() {
		n := v.FieldByName("Nlink")
		if n.IsValid() && n.CanUint() && n.Uint() != 1 {
			return invalid("hardlinked content files refused")
		}
	}
	return nil
}
func syncDir(r *os.Root) error {
	f, e := r.Open(".")
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
