package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Zyko0/go-sdl3/cmd/internal/assets"
)

var (
	regDesktop = regexp.MustCompile(`i([A-Z][A-Za-z_0-9]+)\(`)
	regJsFunc  = regexp.MustCompile(`.*\s=\sfunc`)
	regJS      *regexp.Regexp

	cfg        *assets.Config
	apiRefCode string
)

func True() *bool {
	b := true
	return &b
}

func False() *bool {
	b := false
	return &b
}

type coverage struct {
	Exposed  *bool
	Filename string
	Line     int
}

type refFunc struct {
	CategoryIndex int
	Name          string
	URL           string

	Desktop coverage
	JS      coverage
}

var (
	categories = map[string][]string{
		// IN UPSTREAM ORDER. These name the banner comments in the wiki's
		// QuickReference.md, and they are matched to it BY POSITION: the nth
		// run of // comments in that file is the nth name here. Nothing in the
		// file is parsed to confirm it, because the banners are ASCII art and
		// not worth reading back.
		//
		// So when upstream adds or reorders a section, this list has to follow.
		// checkCategories below refuses to generate anything when the counts
		// disagree, and prints the first function of every section it found so
		// the new one can be placed by name rather than by guesswork.
		"sdl": {
			"Init", "Hints", "Error", "Version", "Properties", "Log", "Video",
			"Events", "Keyboard", "Mouse", "Touch", "Gamepad", "Joystick",
			"Haptic", "Audio", "Time", "Timer", "Render", "SharedObject",
			"Thread", "Mutex", "Atomic", "Filesystem", "IOStream", "AsyncIO",
			"Storage", "Pixels", "Surface", "BlendMode", "Rect", "Camera",
			"MessageBox", "Clipboard", "Dialog", "Tray", "Notification",
			"GPU", "Vulkan", "Metal",
			/*"Platform",*/ "Power", "Sensor", "Process", "Bits", "Endian",
			"Assert", "CPUInfo" /*"Intrinsics",*/, "Locale", "System", "Misc",
			"GUID", "Stdinc",
		},
		"img":   {"Image"},
		"ttf":   {"TTF"},
		"mixer": {"Mixer"},
	}
	collapsedCategories = map[string]struct{}{
		"Error":        {},
		"Version":      {},
		"Log":          {},
		"Time":         {},
		"SharedObject": {},
		"Thread":       {},
		"Mutex":        {},
		"Atomic":       {},
		"Filesystem":   {},
		"Vulkan":       {},
		"Metal":        {},
		"Process":      {},
		"Bits":         {},
		"Endian":       {},
		"Assert":       {},
		"CPUInfo":      {},
		"Intrinsics":   {},
		"Locale":       {},
		"System":       {},
		"Misc":         {},
		"GUID":         {},
		"Stdinc":       {},
	}
	uniqueAPIFunctions = map[string]*refFunc{}
	functions          []*refFunc
)

// checkCategories verifies that the sections parsed out of the wiki line up
// with the names configured above, and explains itself when they do not.
//
// Without this the mismatch surfaced as "index out of range [50] with length
// 50" from deep inside the writer — and only once upstream had added enough
// sections to run off the end. A single added section did something worse and
// quieter: every heading after it was written under the previous section's
// name, and COVERAGE.md was wrong rather than absent.
func checkCategories() error {
	names := categories[cfg.LibraryName]
	parsed := 0
	for _, fn := range functions {
		if fn.CategoryIndex+1 > parsed {
			parsed = fn.CategoryIndex + 1
		}
	}
	if parsed == len(names) {
		return nil
	}

	// The first function of each section is the only reliable way to say which
	// section it is, so it is what gets printed.
	firstOf := make(map[int]string)
	for _, fn := range functions {
		if _, seen := firstOf[fn.CategoryIndex]; !seen {
			firstOf[fn.CategoryIndex] = fn.Name
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s: the wiki has %d sections, this tool is configured with %d.\n",
		cfg.LibraryName, parsed, len(names))
	fmt.Fprintf(&sb, "Update categories[%q] in cmd/coverage/main.go so it matches, in order.\n\n",
		cfg.LibraryName)
	fmt.Fprintf(&sb, "  %-5s %-36s %s\n", "index", "first function in the section", "configured name")
	for i := 0; i < parsed || i < len(names); i++ {
		name := "<missing>"
		if i < len(names) {
			name = names[i]
		}
		fmt.Fprintf(&sb, "  %-5d %-36s %s\n", i, firstOf[i], name)
	}
	return errors.New(sb.String())
}

func AllFunctions() {
	inComments := false
	categoryIndex := -1
	for l := range strings.SplitSeq(apiRefCode, "\n") {
		l = strings.TrimSpace(l)
		l = strings.ReplaceAll(l, "const ", "")
		l = strings.ReplaceAll(l, " * ", "* ")
		l = strings.ReplaceAll(l, " ** ", "** ")
		l = strings.ReplaceAll(l, "* * ", "** ")
		switch {
		case strings.HasPrefix(l, "//"):
			if !inComments {
				categoryIndex++
				inComments = true
			}
			continue
		case strings.HasPrefix(l, "#"):
			continue
		case l == "":
			continue
		default:
			inComments = false
			idx := strings.Index(l, "//")
			if idx != -1 {
				l = l[:idx]
			}
			// Parse function name
			nameIdx := strings.Index(l[1:], cfg.Prefix)
			name := l[nameIdx+1 : strings.Index(l, "(")]
			fn := &refFunc{
				CategoryIndex: categoryIndex,
				Name:          name,
			}
			uniqueAPIFunctions[name] = fn
			functions = append(functions, fn)
		}
	}
}

func main() {
	var (
		configPath string
		dir        string
	)

	flag.StringVar(&configPath, "config", "", "path to config.json file")
	flag.StringVar(&dir, "dir", "", "base directory to generate from/to")
	flag.Parse()

	// Load config
	var err error
	cfg, err = assets.LoadConfig(configPath)
	if err != nil {
		log.Fatal("couldn't parse config file: ", err)
	}

	regJS, err = regexp.Compile(fmt.Sprintf(`"_%s([A-Z][A-Za-z_0-9]+)",`, cfg.Prefix))
	if err != nil {
		log.Fatal(err)
	}

	// Download API ref code
	resp, err := http.Get(cfg.QuickAPIRefURL)
	if err != nil {
		log.Fatal("couldn't download api ref: ", err)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal("couldn't read http response body: ", err)
	}
	apiRefCode = string(b)
	_, apiRefCode, _ = strings.Cut(apiRefCode, "```c")
	apiRefCode, _, _ = strings.Cut(apiRefCode, "```")

	path, err := os.Getwd()
	if err != nil {
		log.Fatal("err: ", err)
	}
	path = filepath.Join(path, dir)

	AllFunctions()
	if err := checkCategories(); err != nil {
		log.Fatal(err)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		log.Fatal("err: ", err)
	}

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") {
			continue
		}

		b, err := os.ReadFile(filepath.Join(path, e.Name()))
		if err != nil {
			log.Fatalf("couldn't read file %s: %v\n", e.Name(), err)
		}

		var inFunc bool
		var braces int
		var funcName string
		var lineIndex int

		var impl *bool

		lines := strings.Split(string(b), "\n")
		isJS := strings.HasPrefix(lines[0], "//go:build js")
		for i, l := range lines {
			if inFunc {
				braces += strings.Count(l, "{")
				braces -= strings.Count(l, "}")
				if braces > 0 {
					switch {
					case !isJS && regDesktop.MatchString(l):
						matches := regDesktop.FindAll([]byte(l), -1)
						for _, m := range matches {
							name := string(m[1 : len(m)-1])
							fn, found := uniqueAPIFunctions[name]
							if !found {
								name = cfg.Prefix + name
								fn, found = uniqueAPIFunctions[name]
							}
							if found {
								funcName = name
								fn.Desktop.Exposed = True()
								fn.Desktop.Line = lineIndex
								fn.Desktop.Filename = dir + "/" + e.Name()
							}
						}
					case isJS && regJS.MatchString(l):
						matches := regJS.FindAll([]byte(l), -1)
						for _, m := range matches {
							name := string(m[6 : len(m)-2])
							fn, found := uniqueAPIFunctions[name]
							if !found {
								name = cfg.Prefix + name
								fn, found = uniqueAPIFunctions[name]
							}
							if found {
								funcName = name
								fn.JS.Line = lineIndex
								fn.JS.Filename = dir + "/" + e.Name()
							}
						}
					case isJS && strings.Contains(l, "panic(\"not implemented on js\")"):
						impl = False()
					case !isJS && strings.Contains(l, "panic(\"not implemented\")"):
						impl = False()
					}
				} else {
					inFunc = false
					braces = 0
					if funcName != "" {
						fn := uniqueAPIFunctions[funcName]
						if impl != nil {
							if isJS {
								fn.JS.Exposed = impl
							} else {
								fn.Desktop.Exposed = impl
							}
						}
					}
					impl = nil
				}
				continue
			}

			if isJS {
				if !regJsFunc.Match([]byte(l)) {
					continue
				}
			} else {
				if !strings.HasPrefix(l, "func ") {
					continue
				}
			}
			inFunc = true
			braces = 1
			funcName = ""
			lineIndex = i
		}
	}
	// Output coverage
	var sb strings.Builder
	categoryIndex := -1

	if cfg.LibraryName == "sdl" {
		sb.WriteString("# API Coverage\n\n")
		sb.WriteString(`
This file tracks the functions that have been wrapped.<br>
The following emojis mean (they are clickable and should link to the code implementation):
- :heavy_check_mark: = implemented
- :x: = not implemented yet
- :question: = not planned / don't know about integrating it or not
`)
	}
	sb.WriteString("<details open>\n")
	sb.WriteString("<summary>")
	sb.WriteString("<h2>" + strings.ToUpper(cfg.LibraryName) + "</h2>")
	sb.WriteString("</summary>\n")
	for _, fn := range functions {
		if fn.CategoryIndex != categoryIndex {
			if categoryIndex != -1 {
				// Close the previous details category
				sb.WriteString("</details>\n")
			}
			categoryIndex = fn.CategoryIndex
			if _, ok := collapsedCategories[categories[cfg.LibraryName][fn.CategoryIndex]]; ok {
				sb.WriteString("<details>\n")
			} else {
				sb.WriteString("<details open>\n")
			}
			sb.WriteString("<summary>")
			sb.WriteString("<h3>" + categories[cfg.LibraryName][fn.CategoryIndex] + "</h3>")
			sb.WriteString("</summary>\n\n")
			sb.WriteString("|Function|Desktop|WASM/js|\n")
			sb.WriteString("|:--|:--:|:--:|\n")
		}

		fn.URL = fmt.Sprintf("https://wiki.libsdl.org/SDL3%s/%s", cfg.URLLibrarySuffix, fn.Name)

		desktop := ":question:"
		js := ":question:"
		if fn.Desktop.Exposed != nil {
			exposedDesktop := *fn.Desktop.Exposed
			if exposedDesktop {
				desktop = ":heavy_check_mark:"
				if fn.JS.Exposed == nil {
					js = ":heavy_check_mark:"
				} else {
					js = ":x:"
				}
			} else {
				desktop = ":x:"
				js = ":x:"
			}
		}
		var urlDesktop, urlJS string
		if fn.Desktop.Filename != "" && fn.Desktop.Line != 0 {
			urlDesktop = fmt.Sprintf("%s#L%d", fn.Desktop.Filename, fn.Desktop.Line)
		}
		if fn.JS.Filename != "" && fn.JS.Line != 0 {
			urlJS = fmt.Sprintf("%s#L%d", fn.JS.Filename, fn.JS.Line)
		} else {
			js = ":question:"
		}
		sb.WriteString(fmt.Sprintf(
			"| [%s](%s) | [%s](%s) | [%s](%s) |\n",
			fn.Name, fn.URL,
			desktop, urlDesktop,
			js, urlJS,
		))
	}
	sb.WriteString("</details>\n")
	sb.WriteString("</details>\n")

	var f *os.File
	if cfg.LibraryName == "sdl" {
		f, err = os.Create("COVERAGE.md")
		if err != nil {
			log.Fatal("couldn't create file: ", err)
		}
	} else {
		f, err = os.OpenFile("COVERAGE.md", os.O_WRONLY|os.O_APPEND, os.ModeAppend)
		if err != nil {
			log.Fatal("couldn't open file: ", err)
		}
	}
	defer f.Close()
	_, err = f.Write([]byte(sb.String()))
	if err != nil {
		log.Fatal("couldn't write file: ", err)
	}
}
