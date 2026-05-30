#!/usr/bin/env ruby
# Adds the native terminal source files to the `remotehost` target's compile
# sources. Idempotent.
require 'xcodeproj'

TARGET = 'remotehost'
FILES  = %w[
  remotehost/RemoteTerminalView.swift
  remotehost/RemoteTerminalViewManager.swift
  remotehost/RemoteTerminalViewManager.m
]

proj_path = File.join(__dir__, 'remotehost.xcodeproj')
project   = Xcodeproj::Project.open(proj_path)
target    = project.targets.find { |t| t.name == TARGET } or abort "target #{TARGET} not found"
group     = project.main_group.find_subpath(TARGET, true)

FILES.each do |rel|
  name = File.basename(rel)
  already = target.source_build_phase.files.any? do |bf|
    bf.file_ref && bf.file_ref.path && File.basename(bf.file_ref.path.to_s) == name
  end
  if already
    puts "already in sources: #{name}"
    next
  end
  ref = group.files.find { |f| f.path && File.basename(f.path.to_s) == name }
  ref ||= group.new_reference(File.join(__dir__, rel))
  target.add_file_references([ref])
  puts "added to sources: #{name}"
end

project.save
puts "saved #{proj_path}"
