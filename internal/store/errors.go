package store

import "fmt"

func errSeedParent(role, parent string) error {
	return fmt.Errorf("store: seed role %q refers to unknown parent %q (parents must come first)", role, parent)
}
