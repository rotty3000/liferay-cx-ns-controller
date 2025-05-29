package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestXxhash3(t *testing.T) {
	type args struct {
		data string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "test1",
			args: args{
				data: "test1",
			},
			want: "1013",
		},
		{
			name: "test2",
			args: args{
				data: "test1test1test1test1test1test1test1test1test1test1test1",
			},
			want: "1119",
		},
		{
			name: "test3",
			args: args{
				data: "",
			},
			want: "0000",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Xxhash3(tt.args.data); got != tt.want {
				t.Errorf("Xxhash3() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVirtuaInstanceIdToNamespace(t *testing.T) {
	type args struct {
		applicationAlias  string
		originNamespace   string
		virtualInstanceID string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name: "test1",
			args: args{
				originNamespace:   "foo",
				virtualInstanceID: "liferay.com",
				applicationAlias:  "default",
			},
			want:    "cx-foo-default-liferaycom",
			wantErr: false,
		},
		{
			name: "test2",
			args: args{
				originNamespace:   "bar",
				virtualInstanceID: "this.is.a.long.domain.name.liferay.com",
				applicationAlias:  "foo",
			},
			want:    "cx-bar-foo-thisisalongdomainnameliferaycom",
			wantErr: false,
		},
		{
			name: "test3",
			args: args{
				originNamespace:   "baz",
				virtualInstanceID: "this.is.a.long.domain.name.that.is.probably.going.to.get.hashed.liferay.com",
				applicationAlias:  "bar",
			},
			want:    "cx-baz-bar-thisisalongdomainnamethatisprobablygoingtogethas1308",
			wantErr: false,
		},
		{
			name: "test4",
			args: args{
				originNamespace:   "fiz",
				virtualInstanceID: "this.is.a.long.domain.name.that.is.probably.going.to.get.hashed.liferay.com",
				applicationAlias:  "thisislonger",
			},
			want:    "cx-fiz-thisislonger-thisisalongdomainnamethatisprobablygoin1308",
			wantErr: false,
		},
		{
			name: "test5",
			args: args{
				originNamespace:   "buz",
				virtualInstanceID: "this.is.a.long.domain.name.that.is.probably.going.to.get.hashed.liferay.com",
				applicationAlias:  "thisislonger.",
			},
			wantErr: true,
		},
		{
			name: "test6",
			args: args{
				originNamespace:   "bof",
				virtualInstanceID: "this.is.a.long.domain.name.that.is.probably.going.to.get.hashed.liferay.com",
				applicationAlias:  "thisiswaytoooooooooooolong",
			},
			wantErr: true,
		},
		{
			name: "test7",
			args: args{
				originNamespace:   "thisisareallylongnamespacenamethatweneedtotruncateit",
				virtualInstanceID: "this.is.a.long.domain.name.that.is.probably.going.to.get.hashed.liferay.com",
				applicationAlias:  "thisislonger",
			},
			want:    "cx-thisisareallylongnam2994-thisislonger-thisisalongdomainn1308",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VirtualInstanceIdToNamespace(tt.args.originNamespace, tt.args.virtualInstanceID, tt.args.applicationAlias)
			if (err != nil) != tt.wantErr {
				t.Errorf("VirtuaInstanceIdToNamespace() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			assert.True(t, len(got) <= 63)
			if got != tt.want {
				t.Errorf("VirtuaInstanceIdToNamespace() = %v, want %v", got, tt.want)
			}
		})
	}
}
