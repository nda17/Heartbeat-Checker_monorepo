package relayguard

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestValidateAcceptsOnlyExactUpstreamTelegramErrorContract(t *testing.T){
	for _,status:=range []int{400,401,404,500}{
		body:=fmt.Sprintf(`{"ok":false,"error_code":%d}`,status)
		if err:=Validate(strings.NewReader(body),status);err!=nil{t.Fatalf("valid upstream status %d rejected: %v",status,err)}
		for _,invalid:=range []string{fmt.Sprintf(`{"ok":true,"error_code":%d}`,status),fmt.Sprintf(`{"ok":false,"error_code":"%d"}`,status),fmt.Sprintf(`{"ok":false,"error_code":%d,"error_code":%d}`,status,status)}{if err:=Validate(strings.NewReader(invalid),status);err==nil{t.Fatalf("invalid upstream response accepted: %s",invalid)}}
	}
}

func TestValidateRejectsOversizedOrMismatchedRelayResponse(t *testing.T){
	valid:=`{"ok":false,"error_code":401}`
	if err:=Validate(strings.NewReader(valid),400);err==nil{t.Fatal("mismatched upstream status accepted")}
	if err:=Validate(bytes.NewReader(bytes.Repeat([]byte(" "),64<<10+1)),401);err==nil{t.Fatal("oversized upstream response accepted")}
}
