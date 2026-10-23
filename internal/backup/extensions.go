package backup

import (
	"encoding/json"
	"path/filepath"
	"regexp"

	"github.com/samber/lo"
)

// Extensions whose stored data --slim drops, so they start with default
// settings after a restore. uBlock Origin stores tens of MB of cached filter
// lists next to its settings.
var slimResetExtensions = []string{"uBlock0@raymondhill.net"}

var extensionUUIDsPref = regexp.MustCompile(`user_pref\("extensions\.webextensions\.uuids",\s*("(?:[^"\\]|\\.)*")\);`)

// extensionUUIDs maps add-on IDs to the internal UUIDs that name their
// storage folders, as recorded in prefs.js.
func extensionUUIDs(profileDir string) map[string]string {
	prefs, _, _ := readFileIfExists(filepath.Join(profileDir, "prefs.js"))
	match := extensionUUIDsPref.FindSubmatch(prefs)
	if match == nil {
		return nil
	}
	var encoded string
	var uuids map[string]string
	if json.Unmarshal(match[1], &encoded) != nil || json.Unmarshal([]byte(encoded), &uuids) != nil {
		return nil
	}
	return uuids
}

// extensionDataPatterns returns exclusion patterns covering everything the
// given extensions have stored in the profile.
func extensionDataPatterns(profileDir string, addonIDs []string) []string {
	uuids := extensionUUIDs(profileDir)
	return lo.FlatMap(addonIDs, func(id string, _ int) []string {
		patterns := []string{"browser-extension-data/" + id}
		if uuid, ok := uuids[id]; ok {
			// Matches both moz-extension+++<uuid> and its ^userContextId=… variant.
			patterns = append(patterns, "storage/default/moz-extension+++"+uuid+"*")
		}
		return patterns
	})
}
