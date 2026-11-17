//go:build !go1.27

package preview

// Match archive/zip's legacy interpretation so preflight scans the same
// directory as the reader it protects, including adversarial shifted archives.
const zip64DirectorySizeSentinel = 0xffff
