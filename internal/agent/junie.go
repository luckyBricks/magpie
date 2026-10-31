package agent

// Junie CLI (JetBrains' terminal agent, `junie`) keeps its settings in
// ~/.junie ($JUNIE_HOME is that folder itself). Its own models are the
// ones its JetBrains account or its BYOK keys give; any other endpoint is
// a custom model profile, models/<id>.json, selected as "custom:<id>". A
// profile holds one primary model and an optional fasterModel, the helper
// it asks for summaries and routing, with the endpoint (baseUrl, the whole
// URL: Junie appends no path), apiType and apiKey shared by both.
// config.json's "model" is the model it starts on.
//
// magpie writes the profile models/magpie.json, speaking chat completions
// to the gateway, with the model picked as primaryModel and the "small"
// pick as fasterModel, and sets config.json's model to custom:magpie. The
// model the user had is stashed and put back when magpie steps out, and
// the profile goes again then. Everything else in config.json (provider,
// byok, proxies, hooks) and the user's other profiles are left alone, as
// is anything the user added to magpie's profile (temperature, extraBody).
// A models/magpie.json that isn't magpie's (its displayName isn't "magpie")
// is the user's: it is refused, never overwritten.
//
// Not tried against a real junie binary yet: docs/subsystems/junie-cli.md
// lists what is open.

import (
	"errors"
	"path/filepath"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/edit"
)

const (
	junieProfile = "magpie"
	junieCustom  = "custom:" + junieProfile
	junieMark    = "magpie" // the profile's displayName: whose it is
)

func junie(home string) *Agent { return junieAt(here(home)) }

func junieAt(at place) *Agent {
	dir := at.getenv("JUNIE_HOME")
	if dir == "" {
		dir = at.join(".junie")
	}
	path := filepath.Join(dir, "config.json")
	prof := filepath.Join(dir, "models", junieProfile+".json")
	keyModel := "junie:" + path + ":model"
	// keyNew marks a config.json magpie made, which goes again once nothing
	// is left in it
	keyNew := "junie:" + path + ":new"
	endpoint := func() string { return at.v1() + "/chat/completions" }
	model := func() string { v, _ := edit.GetJSON(path, "model"); return v }
	pget := func(k string) string { v, _ := edit.GetJSON(prof, k); return v }
	// exists: a profile named magpie is there; ours: and it is magpie's
	exists := func() bool { return isFile(prof) }
	ours := func() bool { return exists() && pget("displayName") == junieMark }
	on := func() bool { return model() == junieCustom && ours() }
	writeProfile := func(ref string) error {
		kvs := []edit.KV{
			{Path: "id", Value: ref},
			{Path: "baseUrl", Value: endpoint()},
			{Path: "apiType", Value: "OpenAICompletion"},
			{Path: "apiKey", Value: at.gwKey()},
			{Path: "displayName", Value: junieMark},
			{Path: "primaryModel.id", Value: ref},
		}
		return edit.SetJSON(prof, kvs...)
	}
	put := func(v string) error {
		if v == "" {
			if !isFile(path) {
				return nil
			}
			return edit.DelJSON(path, "model")
		}
		return edit.SetJSON(path, edit.KV{Path: "model", Value: v})
	}
	return atomic(&Agent{
		ID: "junie", Name: "Junie CLI", Icon: "junie", Aliases: []string{"junie-cli"}, Spelled: prefixed, NoNames: true,
		Bin: "junie", Dir: dir, Path: path,
		Notice: func() string {
			if Running(`(^|/)junie( |$)`) {
				return noticeJunie.String()
			}
			return ""
		},
		Check: func() string {
			if model() != junieCustom {
				return ""
			}
			if !ours() {
				return "Junie's magpie model profile (models/magpie.json) is gone, so it no longer reaches magpie"
			}
			return wiringOff("Junie CLI", prof, func(k string) (string, bool) { return edit.GetJSON(prof, k) },
				"baseUrl", endpoint(), "apiKey", at.gwKey())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				if on() {
					return magpieID + "/" + pget("primaryModel.id")
				}
				return model()
			},
			Set: func(v string) error {
				if ref, ok := cutMagpie(v); ok {
					if exists() && !ours() {
						return errors.New("Junie already has a model profile named magpie (models/magpie.json) that isn't magpie's; rename it first")
					}
					if !on() {
						forget(keyModel, keyNew)
						stash(map[string]string{keyModel: model()})
						if !isFile(path) {
							stash(map[string]string{keyNew: "1"})
						}
					}
					if err := writeProfile(ref); err != nil {
						return err
					}
					return put(junieCustom)
				}
				made := false
				if on() || ours() {
					made = unstash(keyNew) != ""
					// back to what the user had
					if v == "" {
						v = unstash(keyModel)
					} else {
						forget(keyModel)
					}
					if err := edit.Remove(prof); err != nil {
						return err
					}
				}
				if err := put(v); err != nil {
					return err
				}
				if made {
					if raw, err := edit.Read(path); err == nil && len(gjson.ParseBytes(raw).Map()) == 0 {
						return edit.Remove(path)
					}
				}
				return nil
			},
			Options: func(cur map[string]string) []Option {
				var own []Option
				if c := cur["model"]; c != "" && !usesMagpie(c) {
					own = append(own, Option{Value: c, Icon: modelIcon("", c)})
				}
				return append(group("Junie CLI", own), viaMagpie("junie", magpieID+"/")...)
			},
		}, {
			// the helper model Junie asks for summaries and routing
			Key: "small", Label: "small",
			Get: func() string {
				if !on() {
					return ""
				}
				if id := pget("fasterModel.id"); id != "" {
					return magpieID + "/" + id
				}
				return ""
			},
			Set: func(v string) error {
				if !on() {
					return errors.New("connect Junie CLI to magpie first: its helper model is part of magpie's model profile")
				}
				if v == "" {
					return edit.DelJSON(prof, "fasterModel")
				}
				ref, ok := cutMagpie(v)
				if !ok {
					return errors.New("Junie's helper model must be one of magpie's")
				}
				return edit.SetJSON(prof, edit.KV{Path: "fasterModel.id", Value: ref})
			},
			Options: func(map[string]string) []Option { return viaMagpie("junie", magpieID+"/") },
		}},
	}, path, prof)
}

// what junie says after a change (notice.go)
var (
	noticeJunie = newNotice("Junie CLI reads its config at start-up — restart open junie sessions to use this.")
)
