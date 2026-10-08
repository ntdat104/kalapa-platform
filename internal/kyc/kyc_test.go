package kyc

import "testing"

func TestSubmitRequestValidation(t *testing.T) {
	cases := []struct {
		name    string
		req     submitRequest
		wantErr bool
	}{
		{"valid", submitRequest{NationalID: "079123456789", FullName: "Nguyen Van A", DateOfBirth: "1990-05-02"}, false},
		{"valid without dob", submitRequest{NationalID: "123456789", FullName: "Tran B"}, false},
		{"short name", submitRequest{NationalID: "123456789", FullName: "A"}, true},
		{"short national id", submitRequest{NationalID: "12345", FullName: "Tran B"}, true},
		{"long national id", submitRequest{NationalID: "0791234567890", FullName: "Tran B"}, true},
		{"non numeric national id", submitRequest{NationalID: "07912345678X", FullName: "Tran B"}, true},
		{"bad dob format", submitRequest{NationalID: "123456789", FullName: "Tran B", DateOfBirth: "02/05/1990"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.req.validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("validate() error = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}
