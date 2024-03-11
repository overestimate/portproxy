# portproxy
minimal port proxying
## why would you make this
im using this to traverse across my home networks vlans since i have router panel access but cant use ethernet to connect to the guest network
## how to use
1. set up `config.json` (see below)
2. run `portproxy`
3. enjoy proxied connections
## config.json
- `portRange`: ports to use
  - `start`: lower port for port range. this should match your router's "internal port" setting
  - `end`: upper port for port range.
- `autoPort`: if `true`, automatically assigns ports for mappings
- `mappings`: array of port mappings
  - `protocol`: one of `udp`, `tcp`, or `both`
  - `internalIp`: ip of service to connect to. if unspecified, `null`, or `""`, this will default to localhost
  - `port`: port of service to connect to
  - `portOffset`: added to `portRange` start to determine port if auto is false. if `start` is 54000 and `portOffset` is 12, the port will be 54012
- `allowExternalConnectionsFromOwnIp`: if true, allows connections from your own ip (looked up with https://ipify.org)
- `allowNotExplicitDenied`: if true, allows any ip not in the denylist to connect
- `allowLocalhostConnections`: if true, allows localhost to connect to the proxy
- `allowlist`: list of ips to always allow to connect (`denylist` has higher priority)
- `denylist`: list of ips to always deny connection
### unimplemented options
these options exist, but do nothing


