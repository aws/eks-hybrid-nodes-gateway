//go:build e2e

package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/eks-hybrid/test/e2e/constants"
	"github.com/aws/eks-hybrid/test/e2e/suite"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	webhookTestNamespace = "webhook-test-gateway"
	webhookServiceName   = "webhook-svc"
	webhookPodName       = "webhook-server"
	webhookConfigName    = "gateway-webhook-test"
)

func generateWebhookCerts(serviceName, namespace string) (caPEM, certPEM, keyPEM []byte, err error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generating CA key: %w", err)
	}

	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "webhook-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating CA cert: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parsing CA cert: %w", err)
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generating server key: %w", err)
	}

	server := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: fmt.Sprintf("%s.%s.svc", serviceName, namespace)},
		DNSNames: []string{
			serviceName,
			fmt.Sprintf("%s.%s", serviceName, namespace),
			fmt.Sprintf("%s.%s.svc", serviceName, namespace),
			fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace),
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, server, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating server cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshaling server key: %w", err)
	}

	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return
}

const webhookJS = `function handle(r) {
    try {
        var raw = r.requestText || r.requestBody;
        var body = JSON.parse(raw);
        var resp = {
            apiVersion: "admission.k8s.io/v1",
            kind: "AdmissionReview",
            response: { uid: body.request.uid, allowed: true }
        };
        r.headersOut['Content-Type'] = 'application/json';
        r.return(200, JSON.stringify(resp));
    } catch (e) {
        r.error('webhook error: ' + e.toString() + ' body=' + (r.requestText || r.requestBody || 'NULL'));
        r.headersOut['Content-Type'] = 'application/json';
        var errResp = {error: e.toString(), requestBody: (r.requestText || r.requestBody || 'EMPTY').substring(0, 200)};
        r.return(500, JSON.stringify(errResp));
    }
}
export default { handle };`

const webhookNginxConf = `load_module /etc/nginx/modules/ngx_http_js_module.so;
events { worker_connections 64; }
http {
    js_import webhook from /etc/nginx/conf.d/webhook.js;
    server {
        listen 443 ssl;
        client_body_in_single_buffer on;
        client_body_buffer_size 64k;
        client_max_body_size 64k;
        ssl_certificate /etc/webhook/tls/tls.crt;
        ssl_certificate_key /etc/webhook/tls/tls.key;
        location / { js_content webhook.handle; }
    }
}`

func deployWebhookOnHybridNode(ctx context.Context, test *suite.PeeredVPCTest) []byte {
	caPEM, certPEM, keyPEM, err := generateWebhookCerts(webhookServiceName, webhookTestNamespace)
	Expect(err).NotTo(HaveOccurred(), "should generate webhook TLS certs")

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   webhookTestNamespace,
		Labels: map[string]string{"gateway-webhook-test": "true"},
	}}
	_, err = test.K8sClient.Interface.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "should create webhook test namespace")

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "webhook-tls", Namespace: webhookTestNamespace},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": certPEM, "tls.key": keyPEM},
	}
	_, err = test.K8sClient.Interface.CoreV1().Secrets(webhookTestNamespace).Create(ctx, secret, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "should create TLS secret")

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "webhook-config", Namespace: webhookTestNamespace},
		Data:       map[string]string{"nginx.conf": webhookNginxConf, "webhook.js": webhookJS},
	}
	_, err = test.K8sClient.Interface.CoreV1().ConfigMaps(webhookTestNamespace).Create(ctx, cm, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "should create webhook ConfigMap")

	image := fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com/ecr-public/nginx/nginx:latest", constants.EcrAccountId, test.Cluster.Region)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: webhookPodName, Namespace: webhookTestNamespace, Labels: map[string]string{"app": "webhook-test"}},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{"eks.amazonaws.com/compute-type": "hybrid"},
			Tolerations:  []corev1.Toleration{{Key: "eks.amazonaws.com/compute-type", Value: "hybrid", Effect: corev1.TaintEffectNoSchedule}},
			Containers: []corev1.Container{{
				Name:    "webhook",
				Image:   image,
				Command: []string{"nginx", "-c", "/etc/nginx/custom/nginx.conf", "-g", "daemon off;"},
				Ports:   []corev1.ContainerPort{{ContainerPort: 443}},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "tls", MountPath: "/etc/webhook/tls", ReadOnly: true},
					{Name: "config", MountPath: "/etc/nginx/custom/nginx.conf", SubPath: "nginx.conf", ReadOnly: true},
					{Name: "config", MountPath: "/etc/nginx/conf.d/webhook.js", SubPath: "webhook.js", ReadOnly: true},
				},
			}},
			Volumes: []corev1.Volume{
				{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "webhook-tls"}}},
				{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "webhook-config"}}}},
			},
		},
	}
	_, err = test.K8sClient.Interface.CoreV1().Pods(webhookTestNamespace).Create(ctx, pod, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "should create webhook pod")

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: webhookServiceName, Namespace: webhookTestNamespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "webhook-test"},
			Ports:    []corev1.ServicePort{{Port: 443, TargetPort: intstr.FromInt32(443)}},
		},
	}
	_, err = test.K8sClient.Interface.CoreV1().Services(webhookTestNamespace).Create(ctx, svc, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "should create webhook service")

	test.Logger.Info("Waiting for webhook pod to be ready")
	Eventually(func(g Gomega) {
		p, pErr := test.K8sClient.Interface.CoreV1().Pods(webhookTestNamespace).Get(ctx, webhookPodName, metav1.GetOptions{})
		g.Expect(pErr).NotTo(HaveOccurred())
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady {
				g.Expect(c.Status).To(Equal(corev1.ConditionTrue), "pod should be ready, phase: %s", p.Status.Phase)
				return
			}
		}
		g.Expect(true).To(BeFalse(), "pod has no Ready condition yet, phase: %s", p.Status.Phase)
	}).WithTimeout(3*time.Minute).WithPolling(5*time.Second).Should(Succeed(), "webhook pod should become ready")

	return caPEM
}

func registerValidatingWebhook(ctx context.Context, test *suite.PeeredVPCTest, caPEM []byte) {
	fail := admissionv1.Fail
	none := admissionv1.SideEffectClassNone
	webhook := &admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: webhookConfigName},
		Webhooks: []admissionv1.ValidatingWebhook{{
			Name:                    "gateway-test.eks.amazonaws.com",
			AdmissionReviewVersions: []string{"v1"},
			FailurePolicy:           &fail,
			SideEffects:             &none,
			TimeoutSeconds:          aws.Int32(10),
			ClientConfig: admissionv1.WebhookClientConfig{
				Service:  &admissionv1.ServiceReference{Name: webhookServiceName, Namespace: webhookTestNamespace, Path: aws.String("/"), Port: aws.Int32(443)},
				CABundle: caPEM,
			},
			Rules: []admissionv1.RuleWithOperations{{
				Operations: []admissionv1.OperationType{admissionv1.Create},
				Rule:       admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"configmaps"}},
			}},
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"gateway-webhook-test": "true"}},
		}},
	}
	_, err := test.K8sClient.Interface.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, webhook, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "should register validating webhook")
}

func cleanupWebhookTest(ctx context.Context, test *suite.PeeredVPCTest) {
	test.Logger.Info("Cleaning up webhook test resources")
	_ = test.K8sClient.Interface.AdmissionregistrationV1().ValidatingWebhookConfigurations().Delete(ctx, webhookConfigName, metav1.DeleteOptions{})
	_ = test.K8sClient.Interface.CoreV1().Namespaces().Delete(ctx, webhookTestNamespace, metav1.DeleteOptions{})
}
