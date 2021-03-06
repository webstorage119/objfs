///usr/bin/env go run objfs.go registry.go commands.go "$@"; exit

/*
 * objfs.go
 *
 * Copyright 2018 Bill Zissimopoulos
 */
/*
 * This file is part of Objfs.
 *
 * You can redistribute it and/or modify it under the terms of the GNU
 * Affero General Public License version 3 as published by the Free
 * Software Foundation.
 *
 * Licensees holding a valid commercial license may use this file in
 * accordance with the commercial license agreement provided with the
 * software.
 */

package main

import (
	"crypto/rand"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/billziss-gh/golib/appdata"
	"github.com/billziss-gh/golib/cmd"
	"github.com/billziss-gh/golib/config"
	cflag "github.com/billziss-gh/golib/config/flag"
	"github.com/billziss-gh/golib/errors"
	"github.com/billziss-gh/golib/keyring"
	"github.com/billziss-gh/golib/trace"
	"github.com/billziss-gh/golib/util"
	"github.com/billziss-gh/objfs/auth"
	"github.com/billziss-gh/objfs/errno"
	"github.com/billziss-gh/objfs/httputil"
	"github.com/billziss-gh/objfs/objio"
)

// Product variables. These variables can be overriden using the go build
// -ldflags switch. For example:
//
//     go build -ldflags "-X main.MyVersion=0.9"
var (
	MyProductName = "objfs"
	MyDescription = "Object Storage File System"
	MyCopyright   = "2018 Bill Zissimopoulos"
	MyRepository  = "https://github.com/billziss-gh/objfs"
	MyVersion     = "DEVEL"
)

// Configuration variables. These variables control the overall operation of objfs.
//
// The logic of initializing these variables is rather complicated:
//
// - The configuration is determined by a combination of command-line parameters
// and a configuration file. When there is a conflict between the two, the
// command-line parameters take precendence.
//
// - The configuration file is named objfs.conf and placed in the appropriate
// directory for the underlying system, unless the -config command-line parameter
// is specified. The configuration file (if it exists) stores key/value pairs and
// may also have [sections].
//
// - The process starts by creating an empty "flag map" and proceeds by merging
// key/value pairs from the different sources.
//
// - If the configuration file exists it is read and the unnamed empty section ("")
// is merged into the flag map. Then any "-storage" command line parameter
// is merged into the flag map. Then if there is a configuration section with the
// name specified by "storage" that section is merged into the flag map.
//
// - The remaining command-line options (other than -storage) are merged
// into the flag map.
//
// - Finally the flag map is used to initialize the configuration variables.
//
// For the full logic see needvar.
var (
	configPath    string
	dataDir       string
	programConfig config.TypedConfig

	acceptTlsCert  bool
	authName       string
	authSession    auth.Session
	cachePath      string
	credentialPath string
	credentials    auth.CredentialMap
	keyringKind    string
	storage        objio.ObjectStorage
	storageName    string
	storageUri     string
)

func init() {
	flag.CommandLine.Init(flag.CommandLine.Name(), flag.PanicOnError)
	flag.Usage = cmd.UsageFunc()

	flag.StringVar(&configPath, "config", "",
		"`path` to configuration file")
	flag.String("datadir", "",
		"`path` to supporting data and caches")
	flag.BoolVar(&trace.Verbose, "v", false,
		"verbose")

	flag.Bool("accept-tls-cert", false,
		"accept any TLS certificate presented by the server (insecure)")
	flag.String("auth", "",
		"auth `name` to use")
	flag.String("keyring", "user",
		"keyring type to use: system, user, userplain")
	flag.String("credentials", "",
		"auth credentials `path` (keyring:service/user or /file/path)")
	flag.String("storage", defaultStorageName,
		"storage `name` to access")
	flag.String("storage-uri", "",
		"storage `uri` to access")
}

func usage(cmd *cmd.Cmd) {
	if nil == cmd {
		flag.Usage()
	} else {
		cmd.Flag.Usage()
	}
	exit(2)
}

func usageWithError(err error) {
	flag.Usage()
	warn(err)
	exit(2)
}

func initKeyring(kind string, path string) {
	var key []byte

	switch kind {
	case "system":
	case "user":
		pass, err := keyring.Get("objfs", "keyring")
		if nil != err {
			key = make([]byte, 16)
			_, err = rand.Read(key)
			if nil != err {
				fail(err)
			}
			err = keyring.Set("objfs", "keyring", string(key))
			if nil != err {
				fail(err)
			}
		} else {
			key = []byte(pass)
		}
		fallthrough
	case "userplain":
		keyring.DefaultKeyring = &keyring.OverlayKeyring{
			Keyrings: []keyring.Keyring{
				&keyring.FileKeyring{
					Path: filepath.Join(path, "keyring"),
					Key:  key,
				},
				keyring.DefaultKeyring,
			},
		}
	default:
		usageWithError(errors.New("unknown keyring type; specify -keyring in the command line"))
	}
}

var needvarOnce sync.Once

func needvar(args ...interface{}) {
	needvarOnce.Do(func() {
		if "" == configPath {
			dir, err := appdata.ConfigDir()
			if nil != err {
				fail(err)
			}

			configPath = filepath.Join(dir, "objfs.conf")
		}

		flagMap := config.TypedSection{}
		cflag.VisitAll(nil, flagMap,
			"accept-tls-cert",
			"auth",
			"credentials",
			"datadir",
			"keyring",
			"storage",
			"storage-uri")

		c, err := util.ReadFunc(configPath, func(file *os.File) (interface{}, error) {
			return config.ReadTyped(file)
		})
		if nil == err {
			programConfig = c.(config.TypedConfig)

			for k, v := range programConfig[""] {
				flagMap[k] = v
			}

			cflag.Visit(nil, flagMap, "storage")

			for k, v := range programConfig[flagMap["storage"].(string)] {
				flagMap[k] = v
			}

			cflag.Visit(nil, flagMap,
				"accept-tls-cert",
				"auth",
				"credentials",
				"datadir",
				"keyring",
				"storage-uri")
		} else {
			programConfig = config.TypedConfig{}
		}

		acceptTlsCert = flagMap["accept-tls-cert"].(bool)
		authName = flagMap["auth"].(string)
		credentialPath = flagMap["credentials"].(string)
		dataDir = flagMap["datadir"].(string)
		keyringKind = flagMap["keyring"].(string)
		storageName = flagMap["storage"].(string)
		storageUri = flagMap["storage-uri"].(string)

		if "" == dataDir {
			dir, err := appdata.DataDir()
			if nil != err {
				fail(err)
			}

			dataDir = filepath.Join(dir, "objfs")
		}

		initKeyring(keyringKind, dataDir)

		if false {
			fmt.Printf("configPath=%#v\n", configPath)
			fmt.Printf("dataDir=%#v\n", dataDir)
			fmt.Println()
			fmt.Printf("acceptTlsCert=%#v\n", acceptTlsCert)
			fmt.Printf("authName=%#v\n", authName)
			fmt.Printf("credentialPath=%#v\n", credentialPath)
			fmt.Printf("keyringKind=%#v\n", keyringKind)
			fmt.Printf("storageName=%#v\n", storageName)
			fmt.Printf("storageUri=%#v\n", storageUri)
		}

		if acceptTlsCert {
			httputil.DefaultTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
	})

	for _, a := range args {
		switch a {
		case &authName:
			if "" != authName {
				continue
			}
			needvar(&storageName)
			authName = storageName

		case &authSession:
			if nil != authSession {
				continue
			}
			needvar(&authName, &credentials)
			a, err := auth.Registry.NewObject(authName)
			if nil != err {
				usageWithError(errors.New("unknown auth; specify -auth in the command line"))
			}
			s, err := a.(auth.Auth).Session(credentials)
			if nil != err {
				fail(err)
			}
			authSession = s

		case &cachePath:
			if "" != cachePath {
				continue
			}
			needvar(&storageName)
			cachePath = filepath.Join(dataDir, storageName)

		case &credentialPath:
			if "" != credentialPath {
				continue
			}
			needvar(&storageName)
			credentialPath = "keyring:objfs/" + storageName

		case &credentials:
			if nil != credentials {
				continue
			}
			needvar(&credentialPath)
			credentials, _ = auth.ReadCredentials(credentialPath)
			if nil == credentials {
				usageWithError(
					errors.New("unknown credentials; specify -credentials in the command line"))
			}

		case &storageName:
			if "" != storageName {
				continue
			}
			usageWithError(errors.New("unknown storage; specify -storage in the command line"))

		case &storage:
			if nil != storage {
				continue
			}
			var creds interface{}
			if "" != authName {
				needvar(&authSession, &storageName)
				creds = authSession
			} else {
				needvar(&credentials, &storageName)
				creds = credentials
			}
			s, err := objio.Registry.NewObject(storageName, storageUri, creds)
			if nil != err {
				fail(err)
			}
			storage = s.(objio.ObjectStorage)
			if trace.Verbose {
				storage = &objio.TraceObjectStorage{ObjectStorage: storage}
			}
		}
	}
}

func warn(err error) {
	fmt.Fprintf(os.Stderr, "error: %v (%v)\n", err, errno.ErrnoFromErr(err))
}

func fail(err error) {
	warn(err)
	exit(1)
}

type exitcode int

func exit(c int) {
	panic(exitcode(c))
}

func run(self *cmd.CmdMap, flagSet *flag.FlagSet, args []string) (ec int) {
	defer func() {
		if r := recover(); nil != r {
			if c, ok := r.(exitcode); ok {
				ec = int(c)
			} else if _, ok := r.(error); ok {
				ec = 2
			} else {
				panic(r)
			}
		}
	}()

	flagSet.Parse(args)
	arg := flagSet.Arg(0)
	cmd := self.Get(arg)

	if nil == cmd {
		if "help" == arg {
			args = flagSet.Args()[1:]
			if 0 == len(args) {
				flagSet.Usage()
			} else {
				for _, name := range args {
					cmd := self.Get(name)
					if nil == cmd {
						continue
					}
					cmd.Flag.Usage()
				}
			}
		} else {
			flagSet.Usage()
		}
		exit(2)
	}

	cmd.Main(cmd, flagSet.Args()[1:])
	return
}

func addcmd(self *cmd.CmdMap, name string, main func(*cmd.Cmd, []string)) (cmd *cmd.Cmd) {
	c := self.Add(name, main)
	c.Flag.Init(c.Flag.Name(), flag.PanicOnError)
	return c
}

func main() {
	ec := run(cmd.DefaultCmdMap, flag.CommandLine, os.Args[1:])
	if 0 != ec {
		os.Exit(ec)
	}
}
