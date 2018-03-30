// autogenerated from registry.go.in; do not modify

/*
 * registry.go
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
	"github.com/billziss-gh/objfs/fs/objfs"

	"github.com/billziss-gh/objfs.pkg/objio/onedrive"
)

const defaultStorageName = "onedrive"

func init() {
	objfs.Load()

	onedrive.Load()
	storageUriMap["onedrive"] = onedrive.DefaultUri
	authSessionMap["onedrive"] = onedrive.AuthSession
}
