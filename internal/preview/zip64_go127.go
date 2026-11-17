//go:build go1.27

package preview

// Go 1.27 corrected archive/zip's directory-size marker to the 32-bit sentinel.
const zip64DirectorySizeSentinel = 0xffffffff
