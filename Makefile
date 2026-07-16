APP    := FritzTV
DIST   := dist
BUNDLE := $(DIST)/$(APP).app

# libmpv from Homebrew (headers at build time; dylibs get bundled into the .app)
PKG_CONFIG_PATH ?= /opt/homebrew/lib/pkgconfig
export PKG_CONFIG_PATH

.PHONY: run test app dmg clean

run:
	go run -tags pkgconfig .

test:
	go test -tags pkgconfig ./...

app:
	rm -rf $(BUNDLE)
	mkdir -p $(BUNDLE)/Contents/MacOS $(BUNDLE)/Contents/Frameworks
	go build -tags pkgconfig -o $(BUNDLE)/Contents/MacOS/fritztv .
	cp build/Info.plist $(BUNDLE)/Contents/Info.plist
	dylibbundler -od -cd -b \
		-x $(BUNDLE)/Contents/MacOS/fritztv \
		-d $(BUNDLE)/Contents/Frameworks/ \
		-p @executable_path/../Frameworks/
	sh build/fix_rpaths.sh $(BUNDLE)/Contents/MacOS/fritztv $(BUNDLE)/Contents/Frameworks/*.dylib
	# install_name_tool invalidates signatures; ad-hoc re-sign everything (required on arm64)
	find $(BUNDLE)/Contents/Frameworks -name '*.dylib' -exec codesign --force --sign - {} +
	codesign --force --sign - $(BUNDLE)
	@echo "Built $(BUNDLE)"

dmg: app
	rm -rf $(DIST)/dmg-root $(DIST)/$(APP).dmg
	mkdir -p $(DIST)/dmg-root
	cp -R $(BUNDLE) $(DIST)/dmg-root/
	ln -s /Applications $(DIST)/dmg-root/Applications
	hdiutil create -volname "$(APP)" -srcfolder $(DIST)/dmg-root -ov -format UDZO $(DIST)/$(APP).dmg
	rm -rf $(DIST)/dmg-root
	@echo "Built $(DIST)/$(APP).dmg"

clean:
	rm -rf $(DIST) fritztv
