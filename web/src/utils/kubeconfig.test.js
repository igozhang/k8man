import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import {
  lookupHostsIP,
  suggestHostsHostname,
  suggestHostsLine,
  suggestServerFromKubeconfig
} from './kubeconfig.js'

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

  it('falls back to kubeconfig hostname when serverOverride is an IP', () => {
    assert.equal(
      suggestHostsHostname({
        kubeconfig: rancherSample,
        serverOverride: 'https://192.168.8.20/k8s/clusters/local'
      }),
      'rancher-dianbai.sunwoda-evb.com'
    )
  })

  it('looks up existing hosts IP case-insensitively', () => {
    assert.equal(
      lookupHostsIP('Rancher-Dianbai.sunwoda-evb.com.', [
        { hostname: 'rancher-dianbai.sunwoda-evb.com', ip: '192.168.8.20' }
      ]),
      '192.168.8.20'
    )
    assert.equal(lookupHostsIP('missing.example', [{ hostname: 'other', ip: '1.1.1.1' }]), '')
  })

  it('reuses existing hosts IP instead of placeholder', () => {
    assert.deepEqual(
      suggestHostsLine({
        kubeconfig: rancherSample,
        hosts: [{ hostname: 'rancher-dianbai.sunwoda-evb.com', ip: '192.168.8.20' }]
      }),
      {
        hostname: 'rancher-dianbai.sunwoda-evb.com',
        ip: '192.168.8.20',
        line: '192.168.8.20 rancher-dianbai.sunwoda-evb.com',
        fromHosts: true
      }
    )
  })

  it('finds hosts IP via kubeconfig hostname when override is an IP', () => {
    assert.deepEqual(
      suggestHostsLine({
        kubeconfig: rancherSample,
        serverOverride: 'https://10.20.30.40/k8s/clusters/local',
        hosts: [{ hostname: 'rancher-dianbai.sunwoda-evb.com', ip: '192.168.8.20' }]
      }),
      {
        hostname: 'rancher-dianbai.sunwoda-evb.com',
        ip: '192.168.8.20',
        line: '192.168.8.20 rancher-dianbai.sunwoda-evb.com',
        fromHosts: true
      }
    )
  })

  it('falls back to placeholder IP when hosts has no match', () => {
    assert.deepEqual(
      suggestHostsLine({ kubeconfig: rancherSample, hosts: [] }),
      {
        hostname: 'rancher-dianbai.sunwoda-evb.com',
        ip: '10.10.10.10',
        line: '10.10.10.10 rancher-dianbai.sunwoda-evb.com',
        fromHosts: false
      }
    )
  })
})
