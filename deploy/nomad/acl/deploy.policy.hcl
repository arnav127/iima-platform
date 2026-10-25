# For the deploy portal (phase 8) and maintainers deploying by hand until then:
#   nomad acl policy apply -description "deploy apps" deploy deploy.policy.hcl
#   nomad acl token create -name "deploy" -policy deploy
# Super admins use a management token instead.
namespace "default" {
  policy = "write" # submit, revert and stop jobs; read logs; exec into allocations

  variables {
    # App secrets: nomad/jobs/<app id>
    path "nomad/jobs/*" {
      capabilities = ["read", "write", "list", "destroy"]
    }
  }
}

host_volume "*" {
  policy = "write" # create an app's data volume before its first deploy
}

node {
  policy = "read"
}
