package agent

// Junie CLI (JetBrains' terminal agent, `junie`) keeps its settings in
// ~/.junie ($JUNIE_HOME is that folder itself). Its own models are the
// ones its JetBrains account or its BYOK keys give; any other endpoint is
// a custom model profile, models/<id>.json, selected as "custom:<id>". A
// profile holds one primary model and an optional fasterModel, with the
// endpoint (baseUrl, the whole URL: Junie appends no path), apiType and
// apiKey shared by both.
//
// The model Junie starts on is settings.json's modelForLaunch, beside
// providerForLaunch, the BYOK provider it starts on: Junie's own file,
// which it writes too (JunieSettings in junie 26.10.27, 3893.6). Its docs
// say config.json's "model" sets it; 3893.6 doesn't read that (its log's
// model-select line says modelInSettings=null), and a providerForLaunch
// keeps it from a custom model ("Cannot find authorization").
//
// magpie writes the profile models/magpie.json, speaking chat completions
// to the gateway, with the model picked as primaryModel and the "small"
// pick as fasterModel, sets modelForLaunch to custom:magpie and takes
// providerForLaunch out. What the user had in both is stashed and put back
// when magpie steps out, and the profile goes again then. config.json, the
// rest of settings.json and the user's other profiles are left alone, as
// is anything the user added to magpie's profile (temperature, extraBody).
// A models/magpie.json that isn't magpie's (its displayName isn't "magpie")
// is the user's: it is refused, never overwritten.
//
// Tried with junie 3893.6 headless, TUI and ACP: TestJunieRealCLI and
// docs/subsystems/junie-cli.md.

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
	// settings.json's keys for the model and the BYOK provider Junie
	// starts on
	junieLaunch   = "modelForLaunch"
	junieProvider = "providerForLaunch"
)

func junie(home string) *Agent { return junieAt(here(home)) }

func junieAt(at place) *Agent {
	dir := at.getenv("JUNIE_HOME")
	if dir == "" {
		dir = at.join(".junie")
	}
	path := filepath.Join(dir, "settings.json")
	prof := filepath.Join(dir, "models", junieProfile+".json")
	keyModel := "junie:" + path + ":" + junieLaunch
	keyProv := "junie:" + path + ":" + junieProvider
	// keyNew marks a settings.json magpie made, which goes again once
	// nothing is left in it
	keyNew := "junie:" + path + ":new"
	endpoint := func() string { return at.v1() + "/chat/completions" }
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	model := func() string { return get(junieLaunch) }
	pget := func(k string) string { v, _ := edit.GetJSON(prof, k); return v }
	// exists: a profile named magpie is there; ours: and it is magpie's
	exists := func() bool { return isFile(prof) }
	ours := func() bool { return exists() && pget("displayName") == junieMark }
	on := func() bool { return model() == junieCustom && ours() && get(junieProvider) == "" }
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
	// put sets the model Junie starts on, and the provider with it ("" takes
	// a key out)
	put := func(v, prov string) error {
		var kvs []edit.KV
		var del []string
		for _, kv := range [][2]string{{junieLaunch, v}, {junieProvider, prov}} {
			if kv[1] == "" {
				del = append(del, kv[0])
			} else {
				kvs = append(kvs, edit.KV{Path: kv[0], Value: kv[1]})
			}
		}
		if len(del) > 0 && isFile(path) {
			if err := edit.DelJSON(path, del...); err != nil {
				return err
			}
		}
		if len(kvs) == 0 {
			return nil
		}
		return edit.SetJSON(path, kvs...)
	}
	return atomic(&Agent{
		ID: "junie", Name: "Junie CLI", Icon: "junie", Aliases: []string{"junie-cli"}, Spelled: prefixed, NoNames: true,
		// 3893.6: junie-cli-batch:<version>, junie-cli:<version> (TUI),
		// and junie-cli-acp:<version>
		UA:  []string{"junie"},
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
			if p := get(junieProvider); p != "" {
				return "Junie starts on its " + p + " provider (providerForLaunch in settings.json), so it doesn't reach magpie"
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
						forget(keyModel, keyProv, keyNew)
						stash(map[string]string{keyModel: model(), keyProv: get(junieProvider)})
						if !isFile(path) {
							stash(map[string]string{keyNew: "1"})
						}
					}
					if err := writeProfile(ref); err != nil {
						return err
					}
					return put(junieCustom, "")
				}
				made, prov := false, get(junieProvider)
				if on() || ours() {
					made = unstash(keyNew) != ""
					// back to what the user had: their provider whichever
					// model they step out to
					prov = unstash(keyProv)
					if v == "" {
						v = unstash(keyModel)
					} else {
						forget(keyModel)
					}
					if err := edit.Remove(prof); err != nil {
						return err
					}
				}
				if err := put(v, prov); err != nil {
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
			// 3893.6 uses fasterModel for the TUI's helper calls; headless
			// and ACP still use primaryModel for titles and summaries.
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
	noticeJunie = newNotice("Junie CLI reads its settings at start-up — restart open junie sessions to use this.")
)
