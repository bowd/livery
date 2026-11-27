class Livery < Formula
  desc "Declarative multi-host AI agent configuration & profile launcher (liv)"
  homepage "https://github.com/bowd/livery"
  version "0.1.0"
  license "MIT"
  head "https://github.com/bowd/livery.git", branch: "main"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/bowd/livery/releases/download/v#{version}/livery_#{version}_darwin_arm64.tar.gz"
      sha256 "REPLACE_WITH_DARWIN_ARM64_SHA256"
    else
      url "https://github.com/bowd/livery/releases/download/v#{version}/livery_#{version}_darwin_amd64.tar.gz"
      sha256 "REPLACE_WITH_DARWIN_AMD64_SHA256"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/bowd/livery/releases/download/v#{version}/livery_#{version}_linux_arm64.tar.gz"
      sha256 "REPLACE_WITH_LINUX_ARM64_SHA256"
    else
      url "https://github.com/bowd/livery/releases/download/v#{version}/livery_#{version}_linux_amd64.tar.gz"
      sha256 "REPLACE_WITH_LINUX_AMD64_SHA256"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      ENV["CGO_ENABLED"] = "0"
      ldflags = "-s -w -X github.com/bowd/livery/internal/version.Version=#{version}"
      system "go", "build", *std_go_args(output: bin/"liv", ldflags: ldflags), "./cmd/liv"
    else
      bin.install "liv"
    end
  end

  def caveats
    <<~EOS
      Optional companion binary for skill distribution & embedded dashboard:
        brew install skillshare

      If you use Herdr, install the Livery launcher mod:
        liv herdr install-mod
    EOS
  end

  test do
    assert_match "liv ", shell_output("#{bin}/liv version")
  end
end
