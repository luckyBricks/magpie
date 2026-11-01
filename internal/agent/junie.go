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
// magpie writes the profile models/magpie.json to the gateway, with the
// model picked as primaryModel and the "small" pick as fasterModel, sets
// modelForLaunch to custom:magpie and takes providerForLaunch out. What the
// user had in both is stashed and put back when magpie steps out, and the
// profile goes again then. config.json, the rest of settings.json and the
// user's other profiles are left alone, as is anything the user added to
// magpie's profile (temperature, extraBody).
//
// Each role is asked on the API its model's provider serves it on natively
// (junieAPI), so the gateway relays what Junie sent as it is: apiType
// OpenAIResponses to /v1/responses, Anthropic to /v1/messages, else
// OpenAICompletion to /v1/chat/completions. baseUrl is the whole URL, so it
// changes with the API; both roles carry their own baseUrl and apiType (a
// role's overrides the profile's), as the two models may differ. apiKey is
// written literally, but a ${NAME} reference the user put there is theirs
// (Junie resolves it from its environment) and is kept.
// A models/magpie.json that isn't magpie's (its displayName isn't "magpie")
// is the user's: it is refused, never overwritten.
//
// Tried with junie 3893.6, headless (--task), through the gateway to a
// fake vendor: TestJunieRealCLI, and docs/subsystems/junie-cli.md.

import (
	"errors"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
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

// junieEndpoints are the apiTypes magpie writes, each with the gateway path
// Junie sends it to. Junie also takes "Google", which magpie leaves out:
// a model on Gemini's API is asked on Chat.
var junieEndpoints = map[string]string{
	"OpenAICompletion": "/v1/chat/completions",
	"OpenAIResponses":  "/v1/responses",
	"Anthropic":        "/v1/messages",
}

// junieEnvRef is a ${NAME} reference to an environment variable, which
// Junie resolves in apiKey when it loads a profile (and fails to load the
// profile when NAME is not set).
var junieEnvRef = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

// junieAPI is the apiType a model of magpie's is asked on: the API its
// provider serves it on natively, as grokBackend and droid choose it; a
// model whose native API is Chat or not known stays on Chat.
func junieAPI(m catalog.Model) string {
	switch {
	case slices.Contains(m.APIs, string(provider.Responses)):
		return "OpenAIResponses"
	case slices.Contains(m.APIs, string(provider.Anthropic)):
		return "Anthropic"
	}
	return "OpenAICompletion"
}

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
	// apiOf is the apiType the catalog's model ref is asked on
	apiOf := func(ref string) string {
		for _, m := range magpieModels("junie") {
			if m.ID == ref {
				return junieAPI(m)
			}
		}
		return "OpenAICompletion"
	}
	// role is a model role's (primaryModel, fasterModel) own id, baseUrl and
	// apiType for ref
	role := func(k, ref string) []edit.KV {
		api := apiOf(ref)
		return []edit.KV{
			{Path: k + ".id", Value: ref},
			{Path: k + ".baseUrl", Value: at.gw() + junieEndpoints[api]},
			{Path: k + ".apiType", Value: api},
		}
	}
	get := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	model := func() string { return get(junieLaunch) }
	pget := func(k string) string { v, _ := edit.GetJSON(prof, k); return v }
	// exists: a profile named magpie is there; ours: and it is magpie's
	exists := func() bool { return isFile(prof) }
	ours := func() bool { return exists() && pget("displayName") == junieMark }
	on := func() bool { return model() == junieCustom && ours() && get(junieProvider) == "" }
	writeProfile := func(ref string) error {
		// the profile's own are the primary model's (Junie asks for them)
		api := apiOf(ref)
		kvs := []edit.KV{
			{Path: "id", Value: ref},
			{Path: "baseUrl", Value: at.gw() + junieEndpoints[api]},
			{Path: "apiType", Value: api},
			{Path: "displayName", Value: junieMark},
		}
		// a ${NAME} the user put in apiKey is theirs
		if !junieEnvRef.MatchString(pget("apiKey")) {
			kvs = append(kvs, edit.KV{Path: "apiKey", Value: at.gwKey()})
		}
		kvs = append(kvs, role("primaryModel", ref)...)
		// the helper model is asked as the catalog says now, and at this address
		if id := pget("fasterModel.id"); id != "" {
			kvs = append(kvs, role("fasterModel", id)...)
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
		// its requests say junie-cli-batch:<version> when it runs a --task
		// (3893.6); the TUI's weren't seen
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
			// each role is asked at its own address: its baseUrl and apiType,
			// else the profile's, whatever the apiType the user left it on
			inherit := func(r, k string) string {
				if v := pget(r + "." + k); v != "" {
					return v
				}
				return pget(k)
			}
			for _, r := range []string{"primaryModel", "fasterModel"} {
				if r == "fasterModel" && pget("fasterModel.id") == "" {
					continue
				}
				path, ok := junieEndpoints[inherit(r, "apiType")]
				if !ok {
					path = junieEndpoints["OpenAICompletion"]
				}
				base := inherit(r, "baseUrl")
				if msg := wiringOff("Junie CLI", prof, func(string) (string, bool) { return base, base != "" },
					"baseUrl", at.gw()+path); msg != "" {
					return msg
				}
			}
			// a ${NAME} is Junie's to resolve from its own environment: not
			// known from here, so not drift
			return wiringOff("Junie CLI", prof, func(k string) (string, bool) {
				v, ok := edit.GetJSON(prof, k)
				if junieEnvRef.MatchString(v) {
					return at.gwKey(), true
				}
				return v, ok
			}, "apiKey", at.gwKey())
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
			// the helper model the profile names (fasterModel). Junie 3893.6
			// asked the primary model for every helper call it made headless
			// (task titles, step summaries), so this may change nothing yet
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
				return edit.SetJSON(prof, role("fasterModel", ref)...)
			},
			Options: func(map[string]string) []Option { return viaMagpie("junie", magpieID+"/") },
		}},
	}, path, prof)
}

// what junie says after a change (notice.go)
var (
	noticeJunie = newNotice("Junie CLI reads its settings at start-up — restart open junie sessions to use this.")
)
