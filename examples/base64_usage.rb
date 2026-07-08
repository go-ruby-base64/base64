# frozen_string_literal: true
#
# Pure-Ruby usage of the Base64 module, as provided by go-embedded-ruby (rbgo).
# Run it with:  rbgo examples/base64_usage.rb

require "base64"

# Strict (RFC 4648) encode/decode: the standard +/ alphabet, no line breaks.
puts Base64.strict_encode64("hello, dimail")      # => aGVsbG8sIGRpbWFpbA==
puts Base64.strict_decode64("aGVsbG8sIGRpbWFpbA==")

# URL-safe alphabet (-_), padding optional.
puts Base64.urlsafe_encode64("<<foo/bar?>>", padding: false)

# RFC 2045 framing: a newline every 60 output characters (used by e-mail).
puts Base64.encode64("wrapped output " * 6)
