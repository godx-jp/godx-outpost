#!/usr/bin/env ruby
# Adds the SwiftTerm Swift Package (https://github.com/migueldeicaza/SwiftTerm)
# as a dependency of the `remotehost` app target. Idempotent: re-running is a no-op.
#
# Run with the ruby/gems CocoaPods ships:
#   GEM_HOME=/opt/homebrew/Cellar/cocoapods/<ver>/libexec \
#   /opt/homebrew/opt/ruby/bin/ruby ios/add_swiftterm_spm.rb
require 'xcodeproj'

REPO    = 'https://github.com/migueldeicaza/SwiftTerm'
PRODUCT = 'SwiftTerm'
MIN_VER = '1.13.0'
TARGET  = 'remotehost'

proj_path = File.join(__dir__, 'remotehost.xcodeproj')
project   = Xcodeproj::Project.open(proj_path)
target    = project.targets.find { |t| t.name == TARGET } or abort "target #{TARGET} not found"

refs = project.root_object.package_references ||= []
ref  = refs.find { |r| r.respond_to?(:repositoryURL) && r.repositoryURL.to_s.include?('SwiftTerm') }
if ref.nil?
  ref = project.new(Xcodeproj::Project::Object::XCRemoteSwiftPackageReference)
  ref.repositoryURL = REPO
  ref.requirement   = { 'kind' => 'upToNextMajorVersion', 'minimumVersion' => MIN_VER }
  refs << ref
  puts "added remote package reference: #{REPO}"
else
  puts "package reference already present"
end

deps = target.package_product_dependencies
dep  = deps.find { |d| d.product_name == PRODUCT }
if dep.nil?
  dep = project.new(Xcodeproj::Project::Object::XCSwiftPackageProductDependency)
  dep.package      = ref
  dep.product_name = PRODUCT
  deps << dep
  bf = project.new(Xcodeproj::Project::Object::PBXBuildFile)
  bf.product_ref = dep
  target.frameworks_build_phase.files << bf
  puts "linked product #{PRODUCT} to target #{TARGET}"
else
  puts "product dependency already present"
end

project.save
puts "saved #{proj_path}"
