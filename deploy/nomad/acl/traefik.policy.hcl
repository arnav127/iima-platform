# For Traefik's Nomad provider: read the service catalogue, nothing else.
#   nomad acl policy apply -description "traefik reads services" traefik traefik.policy.hcl
#   nomad acl token create -name "traefik" -policy traefik
namespace "default" {
  policy = "read"
}
