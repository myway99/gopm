// Copyright 2014 Unknown
//
// Licensed under the Apache License, Version 2.0 (the "License"): you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

package cmd

import (
	"os"
	"path"
	"strings"

	"github.com/Unknwon/com"
	"github.com/Unknwon/goconfig"
	"github.com/codegangsta/cli"

	"github.com/gpmgo/gopm/modules/doc"
	"github.com/gpmgo/gopm/modules/log"
	"github.com/gpmgo/gopm/modules/setting"
)

var CmdGet = cli.Command{
	Name:  "get",
	Usage: "fetch remote package(s) and dependencies",
	Description: `Command get fetches a package or packages, 
and any pakcage that it or they depend(s) on. 
If the package has a gopmfile, the fetch process will be driven by that.

gopm get
gopm get <import path>@[<tag|commit|branch>:<value>]
gopm get <package name>@[<tag|commit|branch>:<value>]

Can specify one or more: gopm get beego@tag:v0.9.0 github.com/beego/bee

If no version specified and package exists in GOPATH,
it will be skipped, unless user enabled '--remote, -r' option 
then all the packages go into gopm local repository.`,
	Action: runGet,
	Flags: []cli.Flag{
		cli.BoolFlag{"download, d", "download given package only"},
		cli.BoolFlag{"update, u", "update pakcage(s) and dependencies if any"},
		cli.BoolFlag{"local, l", "download all packages to local GOPATH"},
		cli.BoolFlag{"gopath, g", "download all pakcages to GOPATH"},
		cli.BoolFlag{"remote, r", "download all pakcages to gopm local repository"},
		cli.BoolFlag{"verbose, v", "show process details"},
	},
}

var (
	// Saves packages that have been downloaded.
	// NOTE: need a safe map for future downloading packages concurrency.
	downloadCache = make(map[string]bool)
	skipCache     = make(map[string]bool)
	downloadCount int
	failConut     int
)

// downloadPackage downloads package either use version control tools or not.
func downloadPackage(ctx *cli.Context, n *doc.Node) (*doc.Node, []string) {
	log.Message("", "Downloading package: "+n.VerString())
	downloadCache[n.RootPath] = true

	var imports []string
	var err error
	// Check if only need to use VCS tools.
	vcs := doc.GetVcsName(n.InstallGopath)
	// If update, gopath and VCS tools set,
	// then use VCS tools to update the package.
	if ctx.Bool("update") && (ctx.Bool("gopath") || ctx.Bool("local")) && len(vcs) > 0 {
		err = n.UpdateByVcs(vcs)
		imports = doc.GetImports(n.ImportPath, n.RootPath, n.InstallGopath, false)
	} else {
		// IsGetDepsOnly promises package is fixed version and exists in local repository.
		if n.IsGetDepsOnly {
			imports = doc.GetImports(n.ImportPath, n.RootPath, n.InstallPath, false)
		} else {
			// Get revision value from local records.
			if n.IsExist() {
				n.Revision = setting.LocalNodes.MustValue(n.RootPath, "value")
			}
			imports, err = n.Download(ctx)
		}
	}

	if err != nil {
		log.Error("get", "Fail to download pakage: "+n.ImportPath)
		log.Error("", "\t"+err.Error())
		failConut++
		os.RemoveAll(n.InstallPath)
		return nil, nil
	}

	if !n.IsGetDeps {
		imports = nil
	}
	return n, imports
}

// downloadPackages downloads packages with certain commit,
// if the commit is empty string, then it downloads all dependencies,
// otherwise, it only downloada package with specific commit only.
func downloadPackages(target string, ctx *cli.Context, nodes []*doc.Node) {
	for _, n := range nodes {
		// Check if it is a valid remote path or C.
		if n.ImportPath == "C" {
			continue
		} else if !doc.IsValidRemotePath(n.ImportPath) {
			// Invalid import path.
			log.Error("download", "Skipped invalid package: "+n.VerString())
			failConut++
			continue
		}

		// Valid import path.
		if isSubpackage(n.RootPath, target) {
			continue
		}

		// Indicates whether need to download package or update.
		if n.IsFixed() && n.IsExist() {
			n.IsGetDepsOnly = true
		}

		if downloadCache[n.RootPath] {
			if !skipCache[n.RootPath] {
				skipCache[n.RootPath] = true
				log.Trace("Skipped downloaded package: %s", n.VerString())
			}
			continue
		}

		if !ctx.Bool("update") {
			// Check if package has been downloaded.
			if n.IsExist() {
				if !skipCache[n.RootPath] {
					skipCache[n.RootPath] = true
					log.Trace("Skipped installed package: %s", n.VerString())
				}

				// Only copy when no version control.
				if ctx.Bool("gopath") || ctx.Bool("local") {
					n.CopyToGopath()
				}
				continue
			} else {
				setting.LocalNodes.SetValue(n.RootPath, "value", "")
			}
		}
		// Download package.
		nod, imports := downloadPackage(ctx, n)
		for _, name := range imports {
			var gf *goconfig.ConfigFile
			gfPath := path.Join(n.InstallPath, setting.GOPMFILE)

			// Check if has gopmfile.
			if com.IsFile(gfPath) {
				log.Log("Found gopmfile: %s", n.VerString())
				gf = loadGopmfile(gfPath)
			}

			// Need to download dependencies.
			// Generate temporary nodes.
			nodes := make([]*doc.Node, len(imports))
			for i := range nodes {
				nodes[i] = doc.NewNode(name, doc.BRANCH, "", !ctx.Bool("download"))

				if gf == nil {
					continue
				}

				// Check if user specified the version.
				if v := gf.MustValue("deps", imports[i]); len(v) > 0 {
					nodes[i].Type, nodes[i].Value = validPkgInfo(v)
				}
			}
			downloadPackages(target, ctx, nodes)
		}

		// Only save package information with specific commit.
		if nod == nil {
			continue
		}

		// Save record in local nodes.
		log.Success("SUCC", "GET", n.VerString())
		downloadCount++

		// Only save non-commit node.
		if nod.IsEmptyVal() && len(nod.Revision) > 0 {
			setting.LocalNodes.SetValue(nod.RootPath, "value", nod.Revision)
		}

		// If update set downloadPackage will use VSC tools to download the package,
		// else just download to local repository and copy to GOPATH.
		if (ctx.Bool("gopath") || ctx.Bool("local")) && !nod.HasVcs() {
			nod.CopyToGopath()
		}
	}
}

func getPackages(target string, ctx *cli.Context, nodes []*doc.Node) {
	downloadPackages(target, ctx, nodes)
	setting.SaveLocalNodes()

	log.Log("%d package(s) downloaded, %d failed", downloadCount, failConut)
	if ctx.GlobalBool("strict") && failConut > 0 {
		os.Exit(2)
	}
}

func getByGopmfile(ctx *cli.Context) {
	// Make sure gopmfile exists and up-to-date.
	gf, target, imports := genGopmfile()

	// Check if dependency has version.
	nodes := make([]*doc.Node, 0, len(imports))
	for _, name := range imports {
		name = doc.GetRootPath(name)
		n := doc.NewNode(name, doc.BRANCH, "", !ctx.Bool("download"))

		// Check if user specified the version.
		if v := gf.MustValue("deps", name); len(v) > 0 {
			n.Type, n.Value = validPkgInfo(v)
		}
		nodes = append(nodes, n)
	}
	getPackages(target, ctx, nodes)
}

func getByPaths(ctx *cli.Context) {
	nodes := make([]*doc.Node, 0, len(ctx.Args()))
	for _, info := range ctx.Args() {
		pkgPath := info
		n := doc.NewNode(pkgPath, doc.BRANCH, "", !ctx.Bool("download"))

		if i := strings.Index(info, "@"); i > -1 {
			pkgPath = info[:i]
			tp, val := validPkgInfo(info[i+1:])
			n = doc.NewNode(pkgPath, tp, val, !ctx.Bool("download"))
		}

		// Check package name.
		if !strings.Contains(pkgPath, "/") {
			tmpPath := setting.GetPkgFullPath(pkgPath)
			if tmpPath != pkgPath {
				n = doc.NewNode(tmpPath, n.Type, n.Value, n.IsGetDeps)
			}
		}
		nodes = append(nodes, n)
	}
	getPackages(".", ctx, nodes)
}

func runGet(ctx *cli.Context) {
	setup(ctx)

	// Check option conflicts.
	hasConflict := false
	names := ""
	switch {
	case ctx.Bool("local") && ctx.Bool("gopath"):
		hasConflict = true
		names = "'--local, -l' and '--gopath, -g'"
	case ctx.Bool("local") && ctx.Bool("remote"):
		hasConflict = true
		names = "'--local, -l' and '--remote, -r'"
	case ctx.Bool("gopath") && ctx.Bool("remote"):
		hasConflict = true
		names = "'--gopath, -g' and '--remote, -r'"
	}
	if hasConflict {
		log.Error("get", "Command options have conflicts")
		log.Error("", "Following options are not supposed to use at same time:")
		log.Error("", "\t"+names)
		log.Help("Try 'gopm help get' to get more information")
	}

	// Check number of arguments to decide which function to call.
	if len(ctx.Args()) == 0 {
		if ctx.Bool("download") {
			log.Error("get", "Not enough arguments for option:")
			log.Error("", "\t'--download, -d'")
			log.Help("Try 'gopm help get' to get more information")
		}
		getByGopmfile(ctx)
	} else {
		getByPaths(ctx)
	}
}
