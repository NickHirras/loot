package googleplay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/jwt"
)

// DefaultBaseURL is the Cloud Storage JSON API root. Tests point this at an
// httptest server.
const DefaultBaseURL = "https://storage.googleapis.com"

// maxObjectBytes caps a single report download. The largest monthly sales zip
// a hobby developer will ever see is a few megabytes; the cap is there so a
// wrong bucket cannot exhaust memory.
const maxObjectBytes = 256 << 20

// listFields asks GCS for only the object metadata Loot uses. md5Hash is the
// important one: it is how an unchanged monthly file is skipped.
const listFields = "items(name,updated,size,md5Hash),nextPageToken"

// Object is one file in the reporting bucket.
type Object struct {
	Name    string    `json:"name"`
	Updated time.Time `json:"updated"`
	Size    string    `json:"size"`
	MD5Hash string    `json:"md5Hash"`
}

type listResponse struct {
	Items         []Object `json:"items"`
	NextPageToken string   `json:"nextPageToken"`
}

// serviceAccount is the part of a Google service-account key file Loot needs.
type serviceAccount struct {
	Type         string `json:"type"`
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
}

// TokenSourceFromJSON builds a two-legged OAuth2 token source from the bytes
// of a service-account key file.
//
// This deliberately uses golang.org/x/oauth2/jwt rather than
// oauth2/google.JWTConfigFromJSON: the google package reaches for the GCE
// metadata server and so drags cloud.google.com/go into the build, which buys
// Loot nothing — a service-account key file is the only credential Play
// reporting accepts.
func TokenSourceFromJSON(ctx context.Context, data []byte) (oauth2.TokenSource, error) {
	var sa serviceAccount
	if err := json.Unmarshal(data, &sa); err != nil {
		return nil, fmt.Errorf("googleplay: parse service account key: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, fmt.Errorf("googleplay: service account key has no client_email/private_key " +
			"(is it a service account key, and not an OAuth client secret?)")
	}
	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}
	conf := &jwt.Config{
		Email:        sa.ClientEmail,
		PrivateKey:   []byte(sa.PrivateKey),
		PrivateKeyID: sa.PrivateKeyID,
		TokenURL:     tokenURI,
		Scopes:       []string{StorageScope},
	}
	return conf.TokenSource(ctx), nil
}

// NormalizeBucket accepts the bucket id in any of the shapes the Play Console
// hands out: bare, gs:// prefixed, with a trailing slash, or the full "Copy
// Cloud Storage URI" value that includes a report path.
func NormalizeBucket(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "gs://")
	s = strings.TrimPrefix(s, "https://storage.googleapis.com/")
	s = strings.TrimPrefix(s, "https://console.cloud.google.com/storage/browser/")
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		s = s[:i]
	}
	return s
}

// List returns every object under prefix, following pagination. limit caps the
// number of objects returned; 0 means "all of them".
func (s *Source) List(ctx context.Context, prefix string, limit int) ([]Object, error) {
	var out []Object
	pageToken := ""

	for {
		q := url.Values{}
		q.Set("prefix", prefix)
		q.Set("fields", listFields)
		if limit > 0 {
			q.Set("maxResults", fmt.Sprint(limit))
		}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		endpoint := fmt.Sprintf("%s/storage/v1/b/%s/o?%s",
			strings.TrimSuffix(s.baseURL(), "/"), url.PathEscape(s.Bucket), q.Encode())

		body, err := s.do(ctx, endpoint, "list "+prefix)
		if err != nil {
			return nil, err
		}
		var page listResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("googleplay: decode listing of %s: %w", prefix, err)
		}
		out = append(out, page.Items...)

		if limit > 0 && len(out) >= limit {
			return out[:limit], nil
		}
		if page.NextPageToken == "" {
			return out, nil
		}
		pageToken = page.NextPageToken
	}
}

// Download fetches one object's bytes.
func (s *Source) Download(ctx context.Context, name string) ([]byte, error) {
	endpoint := fmt.Sprintf("%s/storage/v1/b/%s/o/%s?alt=media",
		strings.TrimSuffix(s.baseURL(), "/"), url.PathEscape(s.Bucket), url.PathEscape(name))
	return s.do(ctx, endpoint, "download "+name)
}

// do performs one authenticated GET and returns the body, translating the two
// failures a first-time user actually hits into instructions.
func (s *Source) do(ctx context.Context, endpoint, what string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("googleplay: build request: %w", err)
	}
	if s.Tokens != nil {
		tok, err := s.Tokens.Token()
		if err != nil {
			return nil, fmt.Errorf("googleplay: authenticate: %w", err)
		}
		tok.SetAuthHeader(req)
	}
	req.Header.Set("User-Agent", "loot/0.1 (+https://github.com/nickhirras/loot)")

	resp, err := s.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("googleplay: %s: %w", what, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, s.httpError(resp.StatusCode, resp.Status, what, string(snippet))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxObjectBytes))
	if err != nil {
		return nil, fmt.Errorf("googleplay: read %s: %w", what, err)
	}
	return body, nil
}

func (s *Source) httpError(code int, status, what, snippet string) error {
	switch code {
	case http.StatusUnauthorized:
		return fmt.Errorf("googleplay: %s: %s — the service account key was rejected; "+
			"check service_account_json_path points at a current, un-revoked key", what, status)
	case http.StatusForbidden:
		return fmt.Errorf("googleplay: %s: %s — the service account cannot read bucket %s. "+
			"In Play Console → Users and permissions, invite the service account's email "+
			"and grant it Account permissions → \"View app information and download bulk reports\" "+
			"and \"View financial data, orders, and cancellation survey responses\". "+
			"Permission changes can take up to 24 hours to reach the bucket", what, status, s.Bucket)
	case http.StatusNotFound:
		return fmt.Errorf("googleplay: %s: %s — no bucket named %s. "+
			"The id is on Play Console → Download reports → Statistics, behind "+
			"\"Copy Cloud Storage URI\"; it looks like pubsite_prod_rev_01234567890", what, status, s.Bucket)
	default:
		return fmt.Errorf("googleplay: %s: %s: %s", what, status, strings.TrimSpace(snippet))
	}
}
