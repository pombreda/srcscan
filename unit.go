package srcscan

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/build"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// Unit represents a "source unit," such as a Go package, a node.js package, or a Python package.
type Unit interface {
	// Path is the path to this source unit (which is either a directory or file), relative to the
	// scanned directory.
	Path() string
}

func UnitType(unit Unit) string {
	return reflect.TypeOf(unit).Elem().Name()
}

// Units implements sort.Interface.
type Units []Unit

func (u Units) Len() int      { return len(u) }
func (u Units) Swap(i, j int) { u[i], u[j] = u[j], u[i] }
func (u Units) Less(i, j int) bool {
	return fmt.Sprintf("%T", u[i])+u[i].Path() < fmt.Sprintf("%T", u[j])+u[j].Path()
}

// NodeJSPackage represents a node.js package.
type NodeJSPackage struct {
	Dir            string
	PackageJSON    json.RawMessage `json:",omitempty"`
	LibFiles       []string        `json:",omitempty"`
	ScriptFiles    []string        `json:",omitempty"`
	SupportFiles   []string        `json:",omitempty"`
	ExampleFiles   []string        `json:",omitempty"`
	TestFiles      []string        `json:",omitempty"`
	VendorFiles    []string        `json:",omitempty"`
	GeneratedFiles []string        `json:",omitempty"`
}

// Path returns the directory containing the package.json file.
func (u *NodeJSPackage) Path() string {
	return u.Dir
}

type NodeJSPackageConfig struct {
	TestDirs          []string
	TestSuffixes      []string
	SupportDirs       []string
	SupportFilenames  []string
	ExampleDirs       []string
	ScriptDirs        []string
	GeneratedDirs     []string
	GeneratedSuffixes []string
	VendorDirs        []string
}

func readNodeJSPackage(absdir, reldir string, config Config, info os.FileInfo) Unit {
	u := &NodeJSPackage{Dir: reldir}

	// Read package.json.
	var err error
	u.PackageJSON, err = ioutil.ReadFile(filepath.Join(absdir, "package.json"))
	if err != nil {
		panic("read package.json: " + err.Error())
	}

	// Populate *Files fields.
	c := config.NodeJSPackage
	err = filepath.Walk(absdir, func(path string, info os.FileInfo, inerr error) (err error) {
		if info.Mode().IsRegular() && strings.HasSuffix(info.Name(), ".js") {
			relpath, _ := filepath.Rel(absdir, path)
			parts := strings.Split(relpath, "/")
			for _, part := range parts {
				if contains(c.VendorDirs, part) {
					u.VendorFiles = append(u.VendorFiles, relpath)
					return
				} else if contains(c.GeneratedDirs, part) || hasAnySuffix(c.GeneratedSuffixes, relpath) {
					u.GeneratedFiles = append(u.GeneratedFiles, relpath)
					return
				} else if contains(c.ScriptDirs, part) {
					u.ScriptFiles = append(u.ScriptFiles, relpath)
					return
				} else if contains(c.ExampleDirs, part) {
					u.ExampleFiles = append(u.ExampleFiles, relpath)
					return
				} else if contains(c.TestDirs, part) || hasAnySuffix(c.TestSuffixes, relpath) {
					u.TestFiles = append(u.TestFiles, relpath)
					return
				} else if contains(c.SupportDirs, part) || contains(c.SupportFilenames, info.Name()) {
					u.SupportFiles = append(u.SupportFiles, relpath)
					return
				}
			}
			u.LibFiles = append(u.LibFiles, relpath)
		} else if info.IsDir() {
			if info.Name() == "node_modules" {
				return filepath.SkipDir
			}

			// Don't traverse into sub-packages.
			if path != absdir && dirHasFile(path, "package.json") {
				return filepath.SkipDir
			}
		}
		return
	})
	if err != nil {
		panic("scan files: " + err.Error())
	}
	return u
}

// GoPackage represents a Go package.
type GoPackage struct {
	build.Package
}

type GoPackageConfig struct {
	BuildContext build.Context
}

// Path returns the directory that immediately contains the Go package.
func (u *GoPackage) Path() string {
	return u.Dir
}

func readGoPackage(absdir, reldir string, config Config, info os.FileInfo) Unit {
	u := &GoPackage{}
	c := config.GoPackage
	pkg, err := c.BuildContext.ImportDir(absdir, 0)
	if err != nil {
		log.Printf("Warning: error encountered while importing Go package at %s: %s", absdir, err)
	}

	// Try to determine the import path for the package. (Adapted from go/build.)
	srcdirs := c.BuildContext.SrcDirs()
	for i, root := range srcdirs {
		if sub, ok := hasSubdir(root, absdir); ok {
			// We found a potential import path for dir,
			// but check that using it wouldn't find something
			// else first.
			for _, earlyRoot := range srcdirs[:i] {
				if subsrcdir := filepath.Join(earlyRoot, "src", sub); isDir(subsrcdir) {
					goto Found
				}
			}

			// sub would not name some other directory instead of this one.
			// Record it.
			pkg.ImportPath = sub
			pkg.Root = filepath.Dir(root) // without trailing "/src"
			goto Found
		}
	}
Found:

	// Throw away the ImportPos information because it is unlikely to be valuable and requires extra
	// work for test expectations.
	pkg.ImportPos, pkg.TestImportPos, pkg.XTestImportPos = nil, nil, nil

	if config.PathIndependent {
		pkg.Root, pkg.SrcRoot, pkg.PkgRoot, pkg.BinDir = "", "", "", ""
	}

	u.Package = *pkg
	u.Package.Dir = reldir
	return u
}

// PythonPackage represents a Python package.
type PythonPackage struct {
	Dir string
}

// Path returns the directory immediately containing the Python package.
func (u *PythonPackage) Path() string {
	return u.Dir
}

// PythonPackage represents a Python package.
type PythonModule struct {
	File string
}

func (u *PythonModule) Path() string {
	return u.File
}

type MarshalableUnit struct {
	Unit Unit
}

func (mu *MarshalableUnit) MarshalJSON() (data []byte, err error) {
	type unitWithType struct {
		Unit
		Type string
	}
	uwt := unitWithType{mu.Unit, UnitType(mu.Unit)}
	return json.Marshal(uwt)
}

func (mu *MarshalableUnit) UnmarshalJSON(data []byte) (err error) {
	type unitWithType struct {
		Unit json.RawMessage
		Type string
	}
	var uwt unitWithType
	err = json.Unmarshal(data, &uwt)
	if err == nil {
		mu.Unit, err = UnmarshalJSON(uwt.Unit, uwt.Type)
	}
	return
}

var _ json.Marshaler = &MarshalableUnit{}
var _ json.Unmarshaler = &MarshalableUnit{}

// UnmarshalJSON attempts to unmarshal JSON data into a new source unit struct of type unitType.
func UnmarshalJSON(data []byte, unitType string) (unit Unit, err error) {
	switch unitType {
	case "NodeJSPackage":
		unit = &NodeJSPackage{}
	case "GoPackage":
		unit = &GoPackage{}
	case "PythonPackage":
		unit = &PythonPackage{}
	case "PythonModule":
		unit = &PythonModule{}
	default:
		err = errors.New("unhandled source unit type: " + unitType)
	}
	if err == nil {
		err = json.Unmarshal(data, &unit)
	}
	return
}

// Compile-time interface implementation checks.

var _, _, _, _ Unit = &NodeJSPackage{}, &GoPackage{}, &PythonPackage{}, &PythonModule{}
