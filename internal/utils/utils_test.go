package utils

import (
	"testing"
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
			want: "1013772613132772",
		},
		{
			name: "test2",
			args: args{
				data: "test1test1test1test1test1test1test1test1test1test1test1",
			},
			want: "1119602204521400",
		},
		{
			name: "test3",
			args: args{
				data: "",
			},
			want: "0000000000000000",
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

func TestVirtuaInstanceIdToNamespaceSegment(t *testing.T) {
	type args struct {
		virtualInstanceID string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "test1",
			args: args{
				virtualInstanceID: "liferay.com",
			},
			want: "liferaycom",
		},
		{
			name: "test2",
			args: args{
				virtualInstanceID: "this.is.a.long.domain.name.liferay.com",
			},
			want: "thisisalongdomainnameliferaycom",
		},
		{
			name: "test3",
			args: args{
				virtualInstanceID: "this.is.a.long.domain.name.that.is.probably.going.to.get.hashed.liferay.com",
			},
			want: "thisisalongdomainnamethatisprobablygoingt--1308518716147459",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VirtuaInstanceIdToNamespaceSegment(tt.args.virtualInstanceID, 0); got != tt.want {
				t.Errorf("VirtuaInstanceIdToNamespaceSegment() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVirtuaInstanceIdToNamespace(t *testing.T) {
	type args struct {
		virtualInstanceID string
		applicationAlias  string
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
				virtualInstanceID: "liferay.com",
				applicationAlias:  "default",
			},
			want:    "cx-default-liferaycom",
			wantErr: false,
		},
		{
			name: "test2",
			args: args{
				virtualInstanceID: "this.is.a.long.domain.name.liferay.com",
				applicationAlias:  "foo",
			},
			want:    "cx-foo-thisisalongdomainnameliferaycom",
			wantErr: false,
		},
		{
			name: "test3",
			args: args{
				virtualInstanceID: "this.is.a.long.domain.name.that.is.probably.going.to.get.hashed.liferay.com",
				applicationAlias:  "bar",
			},
			want:    "cx-bar-thisisalongdomainnamethatisprobablygoi--1308518716147459",
			wantErr: false,
		},
		{
			name: "test4",
			args: args{
				virtualInstanceID: "this.is.a.long.domain.name.that.is.probably.going.to.get.hashed.liferay.com",
				applicationAlias:  "thisislonger",
			},
			want:    "cx-thisislonger-thisisalongdomainnamethatispr--1308518716147459",
			wantErr: false,
		},
		{
			name: "test5",
			args: args{
				virtualInstanceID: "this.is.a.long.domain.name.that.is.probably.going.to.get.hashed.liferay.com",
				applicationAlias:  "thisislonger.",
			},
			wantErr: true,
		},
		{
			name: "test5",
			args: args{
				virtualInstanceID: "this.is.a.long.domain.name.that.is.probably.going.to.get.hashed.liferay.com",
				applicationAlias:  "thisiswaytoooooooooooolong",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VirtuaInstanceIdToNamespace(tt.args.virtualInstanceID, tt.args.applicationAlias)
			if (err != nil) != tt.wantErr {
				t.Errorf("VirtuaInstanceIdToNamespace() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("VirtuaInstanceIdToNamespace() = %v, want %v", got, tt.want)
			}
		})
	}
}
