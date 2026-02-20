// Copyright 2026 Google LLC
package unused_params

func usedFunc(a int) { // want "parameter a is unused, consider removing or renaming it as _"
}

func Main() {
	usedFunc(1)
}
