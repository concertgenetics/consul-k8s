package connectinject

import (
	"bytes"
	"strings"
	"text/template"

	corev1 "k8s.io/api/core/v1"
)

type initContainerCommandData struct {
	ServiceName     string
	ServicePort     int32
	ServiceProtocol string
	AuthMethod      string
	CentralConfig   bool
	Upstreams       []initContainerCommandUpstreamData
}

type initContainerCommandUpstreamData struct {
	Name       string
	LocalPort  int32
	Datacenter string
	Query      string
}

// containerInit returns the init container spec for registering the Consul
// service, setting up the Envoy bootstrap, etc.
func (h *Handler) containerInit(pod *corev1.Pod) (corev1.Container, error) {
	data := initContainerCommandData{
		ServiceName:     pod.Annotations[annotationService],
		ServiceProtocol: pod.Annotations[annotationProtocol],
		AuthMethod:      h.AuthMethod,
		CentralConfig:   h.CentralConfig,
	}
	if data.ServiceName == "" {
		// Assertion, since we call defaultAnnotations above and do
		// not mutate pods without a service specified.
		panic("No service found. This should be impossible since we default it.")
	}

	// If a port is specified, then we determine the value of that port
	// and register that port for the host service.
	if raw, ok := pod.Annotations[annotationPort]; ok && raw != "" {
		if port, _ := portValue(pod, raw); port > 0 {
			data.ServicePort = port
		}
	}

	// If upstreams are specified, configure those
	if raw, ok := pod.Annotations[annotationUpstreams]; ok && raw != "" {
		for _, raw := range strings.Split(raw, ",") {
			parts := strings.SplitN(raw, ":", 3)

			var datacenter, service_name, prepared_query string
			var port int32
			if parts[0] == "prepared_query" {
				port, _ = portValue(pod, strings.TrimSpace(parts[2]))
				prepared_query = strings.TrimSpace(parts[1])
			} else {
				port, _ = portValue(pod, strings.TrimSpace(parts[1]))
				service_name = strings.TrimSpace(parts[0])

				// parse the optional datacenter
				if len(parts) > 2 {
					datacenter = strings.TrimSpace(parts[2])
				}
			}

			if port > 0 {
				data.Upstreams = append(data.Upstreams, initContainerCommandUpstreamData{
					Name:       service_name,
					LocalPort:  port,
					Datacenter: datacenter,
					Query:      prepared_query,
				})
			}
		}
	}

	// Create expected volume mounts
	volMounts := []corev1.VolumeMount{
		corev1.VolumeMount{
			Name:      volumeName,
			MountPath: "/consul/connect-inject",
		},
	}

	if h.AuthMethod != "" {
		// Extract the service account token's volume mount
		saTokenVolumeMount, err := findServiceAccountVolumeMount(pod)
		if err != nil {
			return corev1.Container{}, err
		}

		// Append to volume mounts
		volMounts = append(volMounts, saTokenVolumeMount)
	}

	// Render the command
	var buf bytes.Buffer
	tpl := template.Must(template.New("root").Parse(strings.TrimSpace(
		initContainerCommandTpl)))
	err := tpl.Execute(&buf, &data)
	if err != nil {
		return corev1.Container{}, err
	}

	return corev1.Container{
		Name:  "consul-connect-inject-init",
		Image: h.ImageConsul,
		Env: []corev1.EnvVar{
			{
				Name: "HOST_IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIP"},
				},
			},
			{
				Name: "POD_IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
				},
			},
			{
				Name: "POD_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
				},
			},
			{
				Name: "POD_NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
				},
			},
		},
		VolumeMounts: volMounts,
		Command:      []string{"/bin/sh", "-ec", buf.String()},
	}, nil
}

// initContainerCommandTpl is the template for the command executed by
// the init container.
const initContainerCommandTpl = `
export CONSUL_HTTP_ADDR="https://${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="https://${HOST_IP}:8502"
export CONSUL_CACERT="/consul/connect-inject/consul_cacert.pem"
export CONSUL_TLS_SERVER_NAME=client.dc1.consul

# Quick and dirty cert creation
cat <<EOF >/consul/connect-inject/consul_cacert.pem
-----BEGIN CERTIFICATE-----
MIIDbjCCAxSgAwIBAgIRAKzTTbp38q5i2SAsNpQb/8MwCgYIKoZIzj0EAwIwgbkx
CzAJBgNVBAYTAlVTMQswCQYDVQQIEwJDQTEWMBQGA1UEBxMNU2FuIEZyYW5jaXNj
bzEaMBgGA1UECRMRMTAxIFNlY29uZCBTdHJlZXQxDjAMBgNVBBETBTk0MTA1MRcw
FQYDVQQKEw5IYXNoaUNvcnAgSW5jLjFAMD4GA1UEAxM3Q29uc3VsIEFnZW50IENB
IDIyOTcyNDM2NjQzMTI1NjE4NzMzMzE0NTQ4MTEyOTk0OTM5NjkzMTAeFw0xOTA2
MDYyMTI4MTJaFw0yNDA2MDQyMTI4MTJaMIG5MQswCQYDVQQGEwJVUzELMAkGA1UE
CBMCQ0ExFjAUBgNVBAcTDVNhbiBGcmFuY2lzY28xGjAYBgNVBAkTETEwMSBTZWNv
bmQgU3RyZWV0MQ4wDAYDVQQREwU5NDEwNTEXMBUGA1UEChMOSGFzaGlDb3JwIElu
Yy4xQDA+BgNVBAMTN0NvbnN1bCBBZ2VudCBDQSAyMjk3MjQzNjY0MzEyNTYxODcz
MzMxNDU0ODExMjk5NDkzOTY5MzEwWTATBgcqhkjOPQIBBggqhkjOPQMBBwNCAAST
cGBvU7AWEis08cELVYEOatzc+SCgKPaJOS3JycTRjMUydJv61K8g9gyx3qhL0iek
dZwzarBKX3PwkAMpgj+ro4H6MIH3MA4GA1UdDwEB/wQEAwIBhjAPBgNVHRMBAf8E
BTADAQH/MGgGA1UdDgRhBF81OTo1YjpmNTo4YzplZDo4OTozZTo0YTpiMTo4NDpk
Njo3MDoxNToxYTo4NDpmMzo1NzpjMzo1MjoyZjplODo3NTo3ZDo3ZTpmOTo3ODo3
ZToxNjoyOToxMTo2Mzo2ZTBqBgNVHSMEYzBhgF81OTo1YjpmNTo4YzplZDo4OToz
ZTo0YTpiMTo4NDpkNjo3MDoxNToxYTo4NDpmMzo1NzpjMzo1MjoyZjplODo3NTo3
ZDo3ZTpmOTo3ODo3ZToxNjoyOToxMTo2Mzo2ZTAKBggqhkjOPQQDAgNIADBFAiEA
zwpWUBilUjyZ8LiwW3DuP7l8dWTMPPwdPoJ08Zpl+NACIDLy/NrL1pI714jqbAxb
BbAhJqk2TTvVzABfPlQQTdGB
-----END CERTIFICATE-----
EOF

# Register the service. The HCL is stored in the volume so that
# the preStop hook can access it to deregister the service.
cat <<EOF >/consul/connect-inject/service.hcl
services {
  id   = "${POD_NAME}-{{ .ServiceName }}-sidecar-proxy"
  name = "{{ .ServiceName }}-sidecar-proxy"
  kind = "connect-proxy"
  address = "${POD_IP}"
  port = 20000

  proxy {
    destination_service_name = "{{ .ServiceName }}"
    destination_service_id = "{{ .ServiceName}}"
    {{ if (gt .ServicePort 0) -}}
    local_service_address = "127.0.0.1"
    local_service_port = {{ .ServicePort }}
    {{ end -}}


    {{ range .Upstreams -}}
    upstreams {
      {{- if .Name }}
      destination_type = "service" 
      destination_name = "{{ .Name }}"
      {{- end}}
      {{- if .Query }}
      destination_type = "prepared_query" 
      destination_name = "{{ .Query}}"
      {{- end}}
      local_bind_port = {{ .LocalPort }}
      {{- if .Datacenter }}
      datacenter = "{{ .Datacenter }}"
      {{- end}}
    }
    {{ end }}
  }

  checks {
    name = "Proxy Public Listener"
    tcp = "${POD_IP}:20000"
    interval = "10s"
    deregister_critical_service_after = "10m"
  }

  checks {
    name = "Destination Alias"
    alias_service = "{{ .ServiceName }}"
  }
}

services {
  id   = "${POD_NAME}-{{ .ServiceName }}"
  name = "{{ .ServiceName }}"
  address = "${POD_IP}"
  port = {{ .ServicePort }}
}
EOF

{{ if .CentralConfig -}}
# Create the central config's service registration
cat <<EOF >/consul/connect-inject/central-config.hcl
kind = "service-defaults"
name = "{{ .ServiceName }}"
protocol = "{{ .ServiceProtocol }}"
EOF
{{- end }}

{{ if .AuthMethod -}}
/bin/consul login -method="{{ .AuthMethod }}" \
  -bearer-token-file="/var/run/secrets/kubernetes.io/serviceaccount/token" \
  -token-sink-file="/consul/connect-inject/acl-token" \
  -meta="pod=${POD_NAMESPACE}/${POD_NAME}"
{{- end }}

{{ if .CentralConfig -}}
/bin/consul config write -cas -modify-index 0 \
  {{- if .AuthMethod }}
  -token-file="/consul/connect-inject/acl-token" \
  {{- end }}
  /consul/connect-inject/central-config.hcl || true
{{- end }}

/bin/consul services register \
  {{- if .AuthMethod }}
  -token-file="/consul/connect-inject/acl-token" \
  {{- end }}
  /consul/connect-inject/service.hcl

# Generate the envoy bootstrap code
/bin/consul connect envoy \
  -proxy-id="${POD_NAME}-{{ .ServiceName }}-sidecar-proxy" \
  {{- if .AuthMethod }}
  -token-file="/consul/connect-inject/acl-token" \
  {{- end }}
  -bootstrap > /consul/connect-inject/envoy-bootstrap.yaml

# Copy the Consul binary
cp /bin/consul /consul/connect-inject/consul
`
