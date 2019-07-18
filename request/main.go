package main

import (
	"context"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Venafi/aws-private-ca-policy-venafi/common"
	"github.com/Venafi/vcert/pkg/certificate"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws/external"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/acmpca"
	"net/http"
)

var (
	// ErrNameNotProvided is thrown when a name is not provided
	ErrNameNotProvided = errors.New("no name was provided in the HTTP body")
)

const (
	acmRequestCertificate  = "CertificateManager.RequestCertificate"
	acmpcaIssueCertificate = "ACMPrivateCA.IssueCertificate"
)

type ACMPCAIssueCertificateRequest struct {
	acmpca.IssueCertificateInput
	VenafiZone string `json:"VenafiZone"`
}

type VenafiRequestCertificateInput struct {
	acm.RequestCertificateInput
	VenafiZone string `json:"VenafiZone"`
}

type ACMPCAIssueCertificateResponse struct {
	CertificateArn string `json:"CertificateArn"`
}

type ACMPCAGetCertificateResponse struct {
	Certificate      string `json:"Certificate"`
	CertificateChain string `json:"CertificateChain"`
}

// ACMPCAHandler is your Lambda function handler
// It uses Amazon API Gateway request/responses provided by the aws-lambda-go/events package,
// However you could use other event sources (S3, Kinesis etc), or JSON-decoded primitive types such as 'string'.
func ACMPCAHandler(request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {

	//TODO: RequestCertificate*|https://docs.aws.amazon.com/acm/latest/APIReference/API_RequestCertificate.html
	//TODO: [*IssueCertificate*|https://docs.aws.amazon.com/acm-pca/latest/APIReference/API_IssueCertificate.html]

	ctx := context.TODO()

	switch request.Headers["X-Amz-Target"] {
	case acmpcaIssueCertificate:
		return venafiACMPCAIssueCertificateRequest(request)
	case acmRequestCertificate:
		return venafiACMRequestCertificate(request)
	case acmpcaListCertificateAuthorities:
		return passThru(request, ctx, acmpcaListCertificateAuthorities)
	case acmpcaGetCertificate:
		return passThru(request, ctx, acmpcaGetCertificate)
	default:
		return clientError(http.StatusMethodNotAllowed, "Can't determine requested method")
	}

}

func venafiACMPCAIssueCertificateRequest(request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {

	var err error
	ctx := context.TODO()
	//TODO: Parse request body with CSR
	var certRequest ACMPCAIssueCertificateRequest
	err = json.Unmarshal([]byte(request.Body), &certRequest)
	if err != nil {
		return clientError(http.StatusUnprocessableEntity, fmt.Sprintf(errUnmarshalJson, acmpcaIssueCertificate, err))
	}

	csr, err := base64.StdEncoding.DecodeString(string(certRequest.Csr))
	if err != nil {
		return clientError(http.StatusUnprocessableEntity, "Can`t decode csr from base64")
	}
	var req certificate.Request
	err = req.SetCSR([]byte(csr))
	if err != nil {
		return clientError(http.StatusUnprocessableEntity, "Can't parse certificate request")
	}

	if certRequest.VenafiZone == "" {
		certRequest.VenafiZone = "Default"
	}

	policy, err := common.GetPolicy(certRequest.VenafiZone)
	if err != nil {
		return clientError(http.StatusFailedDependency, fmt.Sprintf("Failed get policy from database: %s", err))
	}
	err = policy.ValidateCertificateRequest(&req)
	if err != nil {
		return clientError(http.StatusForbidden, err.Error())
	}

	//Issuing ACM certificate
	awsCfg, err := external.LoadDefaultAWSConfig()
	if err != nil {
		return clientError(http.StatusInternalServerError, fmt.Sprintf("Error loading client: %s", err))
	}
	acmCli := acmpca.New(awsCfg)
	caReqInput := acmCli.IssueCertificateRequest(&certRequest.IssueCertificateInput)

	csrResp, err := caReqInput.Send(ctx)
	if err != nil {
		return clientError(http.StatusInternalServerError, fmt.Sprintf("could not get certificate response: %s", err))
	}

	respoBodyJSON, err := json.Marshal(csrResp)
	if err != nil {
		return clientError(http.StatusUnprocessableEntity, fmt.Sprintf("Error marshaling response JSON for target %s: %s", acmpcaIssueCertificate, err))
	}

	return events.APIGatewayProxyResponse{
		Body:       string(respoBodyJSON),
		StatusCode: http.StatusOK,
	}, nil
}

func venafiACMRequestCertificate(request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	ctx := context.TODO()

	var certRequest VenafiRequestCertificateInput
	err := json.Unmarshal([]byte(request.Body), &certRequest)
	if err != nil {
		return clientError(http.StatusUnprocessableEntity, fmt.Sprintf("Error unmarshaling JSON: %s", err))
	}

	var req certificate.Request
	req.Subject = pkix.Name{CommonName: *certRequest.DomainName}
	req.DNSNames = certRequest.SubjectAlternativeNames
	req.CsrOrigin = certificate.ServiceGeneratedCSR

	if certRequest.VenafiZone == "" {
		certRequest.VenafiZone = "Default"
	}
	policy, err := common.GetPolicy(certRequest.VenafiZone)
	if err != nil {
		return clientError(http.StatusFailedDependency, fmt.Sprintf("Failed get policy from database: %s", err))
	}
	err = policy.ValidateCertificateRequest(&req)
	if err != nil {
		return clientError(http.StatusForbidden, err.Error())
	}
	awsCfg, err := external.LoadDefaultAWSConfig()
	if err != nil {
		fmt.Println("Error loading client", err)
	}
	acmCli := acm.New(awsCfg)

	caReqInput := acmCli.RequestCertificateRequest(&certRequest.RequestCertificateInput)

	certResp, err := caReqInput.Send(ctx)
	if err != nil {
		return clientError(http.StatusInternalServerError, fmt.Sprintf("could not get certificate response: %s", err))
	}

	respoBodyJSON, err := json.Marshal(certResp)
	if err != nil {
		return clientError(http.StatusUnprocessableEntity, fmt.Sprintf("Error marshaling response JSON: %s", err))
	}

	return events.APIGatewayProxyResponse{
		Body:       string(respoBodyJSON),
		StatusCode: http.StatusOK,
	}, nil
}

//TODO: Include custom error message into body
func clientError(status int, body string) (events.APIGatewayProxyResponse, error) {
	return events.APIGatewayProxyResponse{
		StatusCode: status,
		Body:       fmt.Sprintf(`{ "msg" : "%s" }`, body),
	}, nil
}

func main() {
	lambda.Start(ACMPCAHandler)
}
