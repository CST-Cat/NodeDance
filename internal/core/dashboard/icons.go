package dashboard

// PreferenceIcons is the Core-owned set of display choices shared by all clients.
// Empty follows the detected Linux distribution for nodes.
func PreferenceIcons() []string {
	return []string{"", "server", "globe", "database", "shield", "terminal", "box", "cloud", "folder", "activity", "docker", "kubernetes", "podman", "portainer", "nginx", "caddy", "apache", "mysql", "postgresql", "mariadb", "redis", "mongodb", "sqlite", "rabbitmq", "grafana", "prometheus", "elasticsearch", "minio", "nextcloud", "gitea", "gitlab", "github", "homeassistant", "linux", "debian", "ubuntu", "linuxmint", "archlinux", "fedora", "centos", "redhat", "almalinux", "opensuse", "suse", "gentoo", "nixos", "manjaro", "freebsd", "openbsd", "deepin", "openwrt", "proxmox", "alpinelinux", "rockylinux", "kalilinux"}
}
