package httptransport

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

func TestHTTPVisibilityRejectsCrossTenantRequest(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	request := httptest.NewRequest("POST", "/api/v1/search/query", nil)
	request = request.WithContext(platformprincipal.WithContext(request.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "membership-1"}))
	context.Request = request
	handler := &Handler{}
	if _, err := handler.visibility(context, "tenant-2"); err == nil {
		t.Fatal("cross-tenant request was accepted")
	}
	visibility, err := handler.visibility(context, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(visibility) != 2 || visibility[0] != "user:user-1" || visibility[1] != "membership:membership-1" {
		t.Fatalf("visibility = %v", visibility)
	}
}
