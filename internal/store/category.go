package store

import (
	"path/filepath"
	"strings"
)

// Category groups downloads by file type and gives each group its own
// folder, the way IDM sorts finished downloads.
type Category struct {
	Name string `json:"name"`
	// Extensions is the list this category claims, without the leading dot.
	Extensions []string `json:"extensions"`
	// Dir is where these files go. A relative path is resolved against the
	// main download directory; an absolute one is used as-is. Empty means
	// the main download directory itself.
	Dir string `json:"dir"`
}

// GeneralCategory is the fallback for anything unclaimed.
const GeneralCategory = "General"

// DefaultCategories mirrors the set IDM ships with.
func DefaultCategories() []Category {
	return []Category{
		{Name: GeneralCategory, Extensions: nil, Dir: ""},
		{Name: "Compressed", Dir: "Compressed", Extensions: []string{
			"7z", "ace", "arj", "bz2", "cab", "gz", "gzip", "iso", "jar",
			"lzh", "lzma", "r0*", "rar", "tar", "tgz", "xz", "z", "zip", "zst",
		}},
		{Name: "Documents", Dir: "Documents", Extensions: []string{
			"csv", "doc", "docx", "epub", "mobi", "odp", "ods", "odt", "pdf",
			"pps", "ppt", "pptx", "ps", "rtf", "tex", "txt", "xls", "xlsx",
		}},
		{Name: "Music", Dir: "Music", Extensions: []string{
			"aac", "aiff", "ape", "flac", "m4a", "mid", "midi", "mp3", "mpa",
			"ogg", "opus", "ra", "wav", "wma",
		}},
		{Name: "Programs", Dir: "Programs", Extensions: []string{
			"apk", "appx", "bat", "bin", "cmd", "deb", "dmg", "exe", "msi",
			"msix", "pkg", "rpm", "sh",
		}},
		{Name: "Video", Dir: "Video", Extensions: []string{
			"3gp", "asf", "avi", "flv", "m2ts", "m4v", "mkv", "mov", "mp4",
			"mpeg", "mpg", "mts", "ogv", "rm", "rmvb", "ts", "vob", "webm", "wmv",
		}},
	}
}

// CategoryFor picks the category a filename belongs to.
func (c Config) CategoryFor(filename string) Category {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	general := Category{Name: GeneralCategory}
	for _, cat := range c.Categories {
		if strings.EqualFold(cat.Name, GeneralCategory) {
			general = cat
		}
		if ext == "" {
			continue
		}
		for _, e := range cat.Extensions {
			if strings.EqualFold(e, ext) {
				return cat
			}
		}
	}
	return general
}

// CategoryByName looks a category up, falling back to General.
func (c Config) CategoryByName(name string) Category {
	general := Category{Name: GeneralCategory}
	for _, cat := range c.Categories {
		if strings.EqualFold(cat.Name, name) {
			return cat
		}
		if strings.EqualFold(cat.Name, GeneralCategory) {
			general = cat
		}
	}
	return general
}

// DirFor resolves where a category's files are saved.
func (c Config) DirFor(cat Category) string {
	if cat.Dir == "" {
		return c.Dir
	}
	if filepath.IsAbs(cat.Dir) {
		return cat.Dir
	}
	return filepath.Join(c.Dir, cat.Dir)
}

// CategoryNames lists the categories in configured order.
func (c Config) CategoryNames() []string {
	out := make([]string, 0, len(c.Categories))
	for _, cat := range c.Categories {
		out = append(out, cat.Name)
	}
	return out
}
