//
//  TimelineHeaderView.swift
//  Sense
//
//  Created by Jimmy Lu on 1/6/17.
//  Copyright © 2017 Hello. All rights reserved.
//

import Foundation

@objcMembers class TimelineHeaderView : UICollectionReusableView {
    
    @objc @IBOutlet weak var historyButton: UIButton!
    @objc @IBOutlet weak var titleLabel: UILabel!
    @objc @IBOutlet weak var shareButton: UIButton!
    
    override func awakeFromNib() {
        super.awakeFromNib()
        self.applyStyle()
    }
    
    @objc func applyStyle() {
        var historyImage = self.historyButton.image(for: UIControl.State.normal)
        historyImage = historyImage?.withRenderingMode(UIImage.RenderingMode.alwaysTemplate)
        self.historyButton.setImage(historyImage, for: UIControl.State.normal)
        
        var shareImage = self.shareButton.image(for: UIControl.State.normal)
        shareImage = shareImage?.withRenderingMode(UIImage.RenderingMode.alwaysTemplate)
        self.shareButton.setImage(shareImage, for: UIControl.State.normal)
        
        let aClass = UINavigationBar.self // this mimics the nav bar
        let tintColor = SenseStyle.color(aClass: aClass, property: .tintColor)
        self.backgroundColor = SenseStyle.color(aClass: aClass, property: .barTintColor)
        self.historyButton.tintColor = tintColor
        self.shareButton.tintColor = tintColor
        self.titleLabel.textColor = SenseStyle.color(aClass: aClass, property: .textColor)
        self.titleLabel.font = SenseStyle.font(aClass: aClass, property: .textFont)
    }
    
}
