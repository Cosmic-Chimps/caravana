package nomadjob

// VolumeName is the Nomad host volume that holds an app's volume:
// "<app>_<volume>". App IDs never contain "_", so the first "_" always ends
// the app ID, and two apps can never share a name.
func VolumeName(appID, volume string) string {
	return appID + "_" + volume
}

// VolumePrefix is what every Nomad host volume of appID starts with.
func VolumePrefix(appID string) string {
	return appID + "_"
}
