package template

import "errors"

// Verify opens the Zeeschuimer extension's IndexedDB in dir and counts its
// object stores. A release template must hold no captured data: items 0,
// uploads 0, navigation records 0 and settings at least 1.
func Verify(dir string) (Counts, error) {
	if err := checkIDBWal(dir); err != nil {
		return Counts{}, err
	}
	_, idbFile, err := findStore(dir)
	if err != nil {
		return Counts{}, err
	}
	counts, err := countStores(idbFile)
	if err != nil {
		return Counts{}, err
	}
	switch {
	case counts.Items > 0:
		return counts, errors.New("template has captured items")
	case counts.Uploads > 0:
		return counts, errors.New("template has upload history")
	case counts.Nav > 0:
		return counts, errors.New("template has navigation history")
	case counts.Settings < 1:
		return counts, errors.New("template has no settings (modules were never enabled)")
	}
	return counts, nil
}
