package snapcraft

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// exportHint is appended to every credential problem, because in every case the
// fix is the same command.
const exportHint = "run `snapcraft export-login --snaps=<name> " +
	"--acls=package_access,package_metrics ./snapcraft-login` again"

// credentials is a ready-to-use Authorization header value plus enough
// provenance to say, in `loot check`, which credential format was recognised.
type credentials struct {
	// Header is the exact value for the Authorization request header.
	Header string
	// Format names the recognised login-file format, for log and check output.
	Format string
	// Email and Expires are informational; both formats may omit them.
	Email   string
	Expires string
}

// loadLogin reads and parses the file written by `snapcraft export-login`.
//
// Three shapes are recognised, which between them cover every snapcraft that
// has shipped an export-login command:
//
//  1. **Ubuntu One / legacy (snapcraft ≤ 6)** — an INI file with a
//     `[login.ubuntu.com]` section holding `macaroon` and `unbound_discharge`.
//     Loot binds the discharge to the root (see macaroon.go) and builds
//     `Authorization: Macaroon root="…", discharge="…"`. This is the format the
//     v1 dashboard API documents and the one Loot is tested against.
//  2. **snapcraft 7+ (craft-store, Ubuntu One)** — the same credential already
//     serialized as the finished header value (`Macaroon root=…, discharge=…`),
//     usually base64-wrapped. Used verbatim; no binding is needed because
//     craft-store bound it at export time.
//  3. **Candid (`SNAPCRAFT_STORE_AUTH=candid`)** — a single bare macaroon
//     token, sent as `Macaroon <token>`. Best effort: candid credentials are
//     minted for the v2 API and the v1 metrics endpoint may reject them, in
//     which case Loot reports the store's own 401/403 and asks for an Ubuntu
//     One export instead.
//
// A JSON object carrying `macaroon`/`unbound_discharge` (or `r`/`d`) is
// accepted wherever the INI is, since several CI helpers emit that instead.
func loadLogin(path string) (credentials, error) {
	if strings.TrimSpace(path) == "" {
		return credentials{}, errors.New("snapcraft: no login_path configured; " + exportHint)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return credentials{}, fmt.Errorf("snapcraft: read login file %s: %w — %s", path, err, exportHint)
	}
	c, err := parseLogin(data)
	if err != nil {
		return credentials{}, fmt.Errorf("snapcraft: login file %s: %w — %s", path, err, exportHint)
	}
	return c, nil
}

// parseLogin turns raw login-file bytes into an Authorization header value.
// Exported behaviour is covered by login_test.go for every supported format.
func parseLogin(data []byte) (credentials, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return credentials{}, errors.New("file is empty")
	}

	if c, err := parseLoginDirect(text); err == nil {
		return c, nil
	} else if !errors.Is(err, errUnrecognised) {
		return credentials{}, err
	}

	// snapcraft 7+ prints the credentials for `SNAPCRAFT_STORE_CREDENTIALS`,
	// and every published recipe base64-wraps them for transport.
	if raw, err := decodeBase64(stripComments(text)); err == nil && len(raw) > 0 {
		inner := strings.TrimSpace(string(raw))
		if c, err := parseLoginDirect(inner); err == nil {
			c.Format += " (base64-wrapped)"
			return c, nil
		}
	}

	// Last resort: a lone macaroon token, which is what candid exports look
	// like. Guarded on shape so binary garbage is reported as unparseable
	// rather than sent to the store as a credential.
	if looksLikeToken(text) {
		return credentials{Header: "Macaroon " + text, Format: "candid macaroon"}, nil
	}
	return credentials{}, errors.New("unrecognised credential format (expected an " +
		"`snapcraft export-login` INI file, its base64 form, or a macaroon token)")
}

// errUnrecognised means "this is not a shape I know", as distinct from "this is
// the right shape but it is broken", which must not fall through to the base64
// and bare-token guesses.
var errUnrecognised = errors.New("unrecognised")

func parseLoginDirect(text string) (credentials, error) {
	switch {
	case strings.HasPrefix(text, "Macaroon "):
		return credentials{Header: text, Format: "exported authorization header"}, nil

	case strings.HasPrefix(text, "{"):
		var obj map[string]any
		if err := json.Unmarshal([]byte(text), &obj); err != nil {
			return credentials{}, fmt.Errorf("%w: %v", errUnrecognised, err)
		}
		root := firstString(obj, "macaroon", "root", "r")
		discharge := firstString(obj, "unbound_discharge", "discharge", "d")
		if root == "" || discharge == "" {
			return credentials{}, errUnrecognised
		}
		return buildUbuntuOne(root, discharge, "JSON export",
			firstString(obj, "email"), firstString(obj, "expires"))

	case strings.HasPrefix(text, "["):
		sections := parseINI(text)
		sec, ok := sections["login.ubuntu.com"]
		if !ok {
			// Any single section is accepted; the header name has moved
			// between snapcraft releases but the keys have not.
			for _, v := range sections {
				sec, ok = v, true
				break
			}
		}
		if !ok {
			return credentials{}, errUnrecognised
		}
		root, discharge := sec["macaroon"], sec["unbound_discharge"]
		if root == "" || discharge == "" {
			return credentials{}, errors.New("section has no macaroon/unbound_discharge pair")
		}
		return buildUbuntuOne(root, discharge, "ubuntu-one export-login", sec["email"], sec["expires"])
	}
	return credentials{}, errUnrecognised
}

func buildUbuntuOne(root, discharge, format, email, expires string) (credentials, error) {
	bound, err := bindDischarge(root, discharge)
	if err != nil {
		return credentials{}, fmt.Errorf("bind discharge: %w", err)
	}
	return credentials{
		Header:  fmt.Sprintf("Macaroon root=%s, discharge=%s", root, bound),
		Format:  format,
		Email:   email,
		Expires: expires,
	}, nil
}

// parseINI is a deliberately tiny reader for the one file shape Loot needs:
// `[section]` headers, `key = value` lines, `#`/`;` comments. Values are not
// unquoted or interpolated, because macaroons are bare base64.
func parseINI(text string) map[string]map[string]string {
	out := map[string]map[string]string{}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := out[section]; !ok {
				out[section] = map[string]string{}
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			continue
		}
		out[section][strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

// stripComments removes the `#` preamble some snapcraft versions print above
// the credentials, and joins the remaining lines: base64 is often hard-wrapped.
func stripComments(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

// looksLikeToken reports whether text could be a serialized macaroon: one long
// run of base64 characters.
func looksLikeToken(text string) bool {
	if len(text) < 64 || strings.ContainsAny(text, " \t\r\n") {
		return false
	}
	for _, r := range text {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '+', r == '/', r == '=':
		default:
			return false
		}
	}
	return true
}

func firstString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := obj[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
