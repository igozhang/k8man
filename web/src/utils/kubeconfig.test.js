import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { suggestHostsHostname, suggestServerFromKubeconfig } from './kubeconfig.js'

const rancherSample = `apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: https://rancher-dianbai.sunwoda-evb.com/k8s/clusters/local
  name: local
contexts:
- context:
    cluster: local
    user: local
  name: local
current-context: local
kind: Config
preferences: {}
users:
- name: local
  user:
    token: x`

describe('kubeconfig hosts hostname', () => {
  it('extracts server from rancher kubeconfig', () => {
    assert.equal(
      suggestServerFromKubeconfig(rancherSample),
      'https://rancher-dianbai.sunwoda-evb.com/k8s/clusters/local'
    )
  })

  it('extracts hostname for hosts line', () => {
    assert.equal(
      suggestHostsHostname({ kubeconfig: rancherSample }),
      'rancher-dianbai.sunwoda-evb.com'
    )
  })

  it('prefers serverOverride', () => {
    assert.equal(
      suggestHostsHostname({
        kubeconfig: rancherSample,
        serverOverride: 'https://override.example.com/'
      }),
      'override.example.com'
    )
  })
})
