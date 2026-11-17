package mesh

// PublicHostNetwork is the name of the Nomad host network that carries a service's published ports
// (spec 041). The first server's Nomad client declares it on the interface of its default route, and
// the translator asks for it by this name, so the two must agree. Renaming it moves every published
// service: their jobs would ask for a network no client declares.
const PublicHostNetwork = "public"
