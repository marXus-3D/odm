package engine

import (
	"mime"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// This file works out what a downloaded file should be called, in the order
// a browser (and IDM) trusts its sources:
//
//	1. a name the caller insisted on
//	2. Content-Disposition, filename* before filename
//	3. the last path segment of the final URL, after redirects
//	4. "download"
//
// and then makes sure the name ends in an extension matching what the server
// said it was sending. Step 4 and the missing extension are what produced
// "random names with no extension": a CDN URL whose path is a signed token,
// or ends in /videoplayback, carries no name at all.

// typeExt maps a content type to the extension to use for it. The list is
// short on purpose: it covers what actually arrives from a browser hand-off.
// Anything not here falls through to the standard library's table.
var typeExt = map[string]string{
	"application/gzip":                        ".gz",
	"application/java-archive":                ".jar",
	"application/json":                        ".json",
	"application/pdf":                         ".pdf",
	"application/rtf":                         ".rtf",
	"application/vnd.android.package-archive": ".apk",
	"application/vnd.ms-excel":                ".xls",
	"application/vnd.ms-powerpoint":           ".ppt",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ".xlsx",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   ".docx",
	"application/x-7z-compressed":                                               ".7z",
	"application/x-bzip2":                                                       ".bz2",
	"application/x-debian-package":                                              ".deb",
	"application/x-iso9660-image":                                               ".iso",
	"application/x-msdownload":                                                  ".exe",
	"application/x-msi":                                                         ".msi",
	"application/x-rar-compressed":                                              ".rar",
	"application/x-redhat-package-manager":                                      ".rpm",
	"application/x-tar":                                                         ".tar",
	"application/x-xz":                                                          ".xz",
	"application/x-zip-compressed":                                              ".zip",
	"application/zip":                                                           ".zip",
	"application/zstd":                                                          ".zst",
	"audio/aac":                                                                 ".aac",
	"audio/flac":                                                                ".flac",
	"audio/mp4":                                                                 ".m4a",
	"audio/mpeg":                                                                ".mp3",
	"audio/ogg":                                                                 ".ogg",
	"audio/opus":                                                                ".opus",
	"audio/wav":                                                                 ".wav",
	"audio/x-wav":                                                               ".wav",
	"image/gif":                                                                 ".gif",
	"image/jpeg":                                                                ".jpg",
	"image/png":                                                                 ".png",
	"image/svg+xml":                                                             ".svg",
	"image/webp":                                                                ".webp",
	"text/css":                                                                  ".css",
	"text/csv":                                                                  ".csv",
	"text/html":                                                                 ".html",
	"text/plain":                                                                ".txt",
	"video/3gpp":                                                                ".3gp",
	"video/mp2t":                                                                ".ts",
	"video/mp4":                                                                 ".mp4",
	"video/mpeg":                                                                ".mpeg",
	"video/ogg":                                                                 ".ogv",
	"video/quicktime":                                                           ".mov",
	"video/webm":                                                                ".webm",
	"video/x-matroska":                                                          ".mkv",
	"video/x-msvideo":                                                           ".avi",
}

// vagueTypes say "some bytes" and so tell us nothing about the extension.
// Guessing ".bin" from one of these would be worse than leaving the name be.
var vagueTypes = map[string]bool{
	"application/octet-stream":   true,
	"binary/octet-stream":        true,
	"application/binary":         true,
	"application/x-binary":       true,
	"application/download":       true,
	"application/force-download": true,
	"application/unknown":        true,
	"":                           true,
}

// scriptExt are the extensions of things that generate a response rather
// than being one. A URL ending in download.php is not a PHP file.
var scriptExt = map[string]bool{
	"php": true, "php3": true, "php4": true, "php5": true, "phtml": true,
	"asp": true, "aspx": true, "ashx": true, "axd": true, "jsp": true,
	"jspx": true, "cgi": true, "pl": true, "py": true, "rb": true,
	"do": true, "action": true, "cfm": true,
}

// plausibleExt is what an extension looks like: letters then letters or
// digits, short. It rejects the tail of a name like "clip.2024-05-01" and
// the hash on the end of a cache-busted path.
var plausibleExt = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,7}$`)

// knownExt is the stricter test, for deciding whether a segment in the
// middle of a URL path is a filename or just another path component. Every
// extension the type table can produce, plus the ones that arrive under
// application/octet-stream and so are never named by a content type.
var knownExt = func() map[string]bool {
	m := map[string]bool{}
	for _, e := range typeExt {
		m[strings.TrimPrefix(e, ".")] = true
	}
	for _, e := range []string{
		"7z", "aac", "apk", "avi", "bin", "bz2", "cab", "dmg", "doc", "docx",
		"epub", "exe", "flac", "flv", "gz", "img", "iso", "jar", "m4a", "m4v",
		"mkv", "mobi", "mov", "mp3", "mp4", "mpg", "msi", "msix", "ogg", "opus",
		"pdf", "pkg", "ppt", "pptx", "rar", "rpm", "tar", "tgz", "ts", "txt",
		"vhd", "wav", "webm", "wma", "wmv", "xls", "xlsx", "xz", "zip", "zst",
	} {
		m[e] = true
	}
	return m
}()

// ExtForContentType returns the extension to use for a Content-Type header,
// with the leading dot, or "" when the type does not pin one down.
func ExtForContentType(contentType string) string {
	ct := contentType
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.ToLower(strings.TrimSpace(ct))
	if vagueTypes[ct] {
		return ""
	}
	if ext, ok := typeExt[ct]; ok {
		return ext
	}
	// The standard library knows more types than are worth listing here, but
	// its answers come partly from the Windows registry and arrive unsorted,
	// so take the shortest sane one for the same result on every machine.
	best := ""
	exts, err := mime.ExtensionsByType(ct)
	if err != nil {
		return ""
	}
	for _, e := range exts {
		if !plausibleExt.MatchString(strings.TrimPrefix(e, ".")) {
			continue
		}
		if best == "" || len(e) < len(best) || (len(e) == len(best) && e < best) {
			best = e
		}
	}
	return strings.ToLower(best)
}

// withExtension gives a name an extension when it has none, or replaces one
// that belongs to a script rather than to a file. A name already ending in
// something believable is left alone: servers get Content-Type wrong far
// more often than they get the name wrong.
func withExtension(name, contentType string) string {
	cur := strings.TrimPrefix(path.Ext(name), ".")
	lower := strings.ToLower(cur)
	if plausibleExt.MatchString(cur) && !scriptExt[lower] {
		return name
	}
	want := ExtForContentType(contentType)
	if want == "" {
		return name
	}
	if cur != "" && strings.EqualFold("."+cur, want) {
		return name
	}
	if scriptExt[lower] {
		name = strings.TrimSuffix(name, path.Ext(name))
	}
	return name + want
}

// dispositionParam finds a filename in a Content-Disposition header that
// mime.ParseMediaType refused. Real servers send unquoted names with spaces,
// raw UTF-8 bytes and stray backslashes, any of which makes the strict
// parser give up on the whole header and hand back nothing.
var dispositionParam = regexp.MustCompile(`(?i)\bfilename(\*?)\s*=\s*("[^"]*"|[^;]+)`)

// FilenameFromDisposition pulls the name out of a Content-Disposition
// header, preferring the RFC 5987 filename* form. It returns "" when there
// is no usable name.
func FilenameFromDisposition(cd string) string {
	if strings.TrimSpace(cd) == "" {
		return ""
	}
	if _, params, err := mime.ParseMediaType(cd); err == nil {
		if v := params["filename*"]; v != "" {
			if dec, err := decodeExtValue(v); err == nil && dec != "" {
				return dec
			}
		}
		if v := params["filename"]; v != "" {
			return decodeName(v)
		}
	}

	var plain, star string
	for _, m := range dispositionParam.FindAllStringSubmatch(cd, -1) {
		v := strings.Trim(strings.TrimSpace(m[2]), `"`)
		if v == "" {
			continue
		}
		if m[1] == "*" {
			if dec, err := decodeExtValue(v); err == nil && dec != "" && star == "" {
				star = dec
			}
			continue
		}
		if plain == "" {
			plain = decodeName(v)
		}
	}
	if star != "" {
		return star
	}
	return plain
}

// decodeName undoes the two encodings that turn up in a plain filename
// parameter: RFC 2047 encoded words, and percent-encoding.
func decodeName(v string) string {
	var dec mime.WordDecoder
	if out, err := dec.DecodeHeader(v); err == nil && out != "" {
		v = out
	}
	if strings.Contains(v, "%") {
		if out, err := url.PathUnescape(v); err == nil && out != "" && utf8.ValidString(out) {
			v = out
		}
	}
	return v
}

// nameParams are the query parameters that carry a filename. S3, GCS and
// friends pass one to override Content-Disposition on a signed URL.
var nameParams = map[string]bool{
	"filename": true, "file_name": true, "fname": true, "name": true,
	"title": true, "download": true, "attachment": true, "file": true,
	"downloadname": true, "originalfilename": true,
}

// dispositionParams carry a whole Content-Disposition header in the query
// string, which is how a presigned URL names its file.
var dispositionParams = map[string]bool{
	"response-content-disposition": true,
	"rscd":                         true,
	"content-disposition":          true,
}

// FilenameFromURL finds the name a URL is carrying, or "" when it carries
// none. It is more than the last path segment, because a CDN routinely puts
// the name in the middle of the path and a routing verb on the end:
//
//	/file/f6233603-074b-4422-affc-a558b986e565/artifact/video_1280.mp4/binary/cdn
//
// The name there is video_1280.mp4; the last segment is "cdn". So: a query
// parameter first, then the last segment if it already looks like a file,
// then any earlier segment ending in an extension we recognise, and only
// then the last segment whatever it is.
func FilenameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return FilenameFromParsedURL(u)
}

// FilenameFromParsedURL is FilenameFromURL for a URL that is already parsed,
// which is what the probe has after following redirects.
func FilenameFromParsedURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	if n := nameFromQuery(u.Query()); n != "" {
		return n
	}

	segs := strings.Split(u.Path, "/")
	last := -1
	for i := len(segs) - 1; i >= 0; i-- {
		s := unescapeSegment(segs[i])
		if s == "" || s == "." || s == ".." {
			continue
		}
		if last < 0 {
			last = i
			// The end of the path is the usual place, and anything that
			// looks like a file there is taken at face value.
			ext := strings.ToLower(strings.TrimPrefix(path.Ext(s), "."))
			if plausibleExt.MatchString(ext) && !scriptExt[ext] {
				return s
			}
			continue
		}
		// Further in, the test is stricter: a path component may well have a
		// dot in it without being a file, so only an extension we actually
		// recognise counts.
		if knownExt[strings.ToLower(strings.TrimPrefix(path.Ext(s), "."))] {
			return s
		}
	}
	if last >= 0 {
		return unescapeSegment(segs[last])
	}
	return ""
}

// nameFromQuery reads a filename out of the query string, if one is there.
func nameFromQuery(q url.Values) string {
	for k, vs := range q {
		key := strings.ToLower(k)
		if !dispositionParams[key] {
			continue
		}
		for _, v := range vs {
			if n := FilenameFromDisposition(v); n != "" {
				return n
			}
		}
	}
	for k, vs := range q {
		if !nameParams[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			v = strings.TrimSpace(decodeName(v))
			if i := strings.LastIndexAny(v, `/\`); i >= 0 {
				v = v[i+1:]
			}
			// These parameters hold ids as often as names, so one is only
			// believed when it ends in something recognisable.
			if knownExt[strings.ToLower(strings.TrimPrefix(path.Ext(v), "."))] {
				return v
			}
		}
	}
	return ""
}

func unescapeSegment(s string) string {
	if un, err := url.PathUnescape(s); err == nil {
		return un
	}
	return s
}

// decodeExtValue decodes an RFC 5987 ext-value: charset'lang'pct-encoded.
func decodeExtValue(v string) (string, error) {
	parts := strings.SplitN(v, "'", 3)
	if len(parts) != 3 {
		return url.PathUnescape(v)
	}
	dec, err := url.PathUnescape(parts[2])
	if err != nil {
		return "", err
	}
	if strings.EqualFold(parts[0], "iso-8859-1") {
		var b strings.Builder
		for i := 0; i < len(dec); i++ {
			b.WriteRune(rune(dec[i]))
		}
		dec = b.String()
	}
	return dec, nil
}
