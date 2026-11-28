// Package selfbackup is the pure part of Caravana backing itself up (feature 063): which Nomad Variables
// are kept, the recovery key, and the backup file: a plain header and an encrypted body that holds the
// database, the variables and a manifest. It reads and writes streams and does nothing else: no network,
// no store, no service, no Nomad (a test keeps it so).
//
// Caravana keeps only the public half of the recovery key. Each backup makes an ephemeral X25519 key
// pair, so what is in the bucket can be read only by someone who holds the owner's key, and a stolen
// server doesn't give the old backups' secrets away.
package selfbackup
