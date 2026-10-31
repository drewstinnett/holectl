package cli

func buildTree() *Command {
	root := &Command{
		Name:    "holectl",
		Summary: "holectl (\"hole control\") drives a Pi-hole v6 server through its REST API.",
		Long: `Output is a table on a terminal and the API's own JSON otherwise (force with -o json|table).
Settings come from flags, then HOLECTL_* env vars, then ~/.config/holectl/config.json.
Start with: holectl config init --url https://pi.hole --insecure --password-stdin`,
		Subs: []*Command{
			blockingCmd(),
			statsCmd(),
			historyCmd(),
			queriesCmd(),
			searchCmd(),
			domainsCmd(),
			localDNSCmd(),
			listsCmd(),
			groupsCmd(),
			clientsCmd(),
			infoCmd(),
			networkCmd(),
			dhcpCmd(),
			logsCmd(),
			serverConfigCmd(),
			actionCmd(),
			teleporterCmd(),
			paddCmd(),
			endpointsCmd(),
			apiCmd(),
			authCmd(),
			configCmd(),
			skillCmd(),
			versionCmd(),
			updateCmd(),
			helpCmd(),
		},
	}
	root.link()
	return root
}
