package sms

import "fmt"

// Text builds SPEC §9.2's fixed message templates. baseURL must already be
// scheme+host with no trailing slash (SPEC §12.3 BASE_URL).
//
//	발령:  [{발령종류}] {메시지}\n임무확인·위치보고 {BASE_URL}/f
//	종료:  [상황종료] {발령종류} 상황이 종료되었습니다. 위치 수집을 중단합니다.
//	재발송: [{발령종류}·재안내] 아직 접속하지 않으셨습니다. {BASE_URL}/f
//
// The link is always the same public path — SPEC: "개인 식별 정보를 URL에
// 넣지 않는다".
func OpenText(baseURL, incidentType, message string) string {
	return fmt.Sprintf("[%s] %s\n임무확인·위치보고 %s/f", incidentType, message, baseURL)
}

func CloseText(incidentType string) string {
	return fmt.Sprintf("[상황종료] %s 상황이 종료되었습니다. 위치 수집을 중단합니다.", incidentType)
}

func ResendText(baseURL, incidentType string) string {
	return fmt.Sprintf("[%s·재안내] 아직 접속하지 않으셨습니다. %s/f", incidentType, baseURL)
}

// euckrLen estimates a string's byte length as EUC-KR (2 bytes for most
// Korean/Latin BMP characters actually used here; ASCII stays 1 byte),
// which is the reference encoding text-messaging byte budgets are quoted
// in (SPEC §9.2: "90바이트(EUC-KR 기준) 초과 시 장문(LMS)").
func euckrLen(s string) int {
	n := 0
	for _, r := range s {
		if r < 0x80 {
			n++
		} else {
			n += 2
		}
	}
	return n
}

// IsLMS reports whether text exceeds the SMS (non-LMS) byte budget.
func IsLMS(text string) bool { return euckrLen(text) > 90 }
